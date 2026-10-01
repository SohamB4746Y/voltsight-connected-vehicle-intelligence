package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	chargerv1 "voltsight/gen/voltsight/charger/v1"
	"voltsight/internal/kafkautil"
)

const chargerStatusHash = "chargers:status"

type chargerRow struct {
	ID      uuid.UUID  `json:"id"`
	Name    string     `json:"name"`
	Public  bool       `json:"public"`
	Lat     float64    `json:"lat"`
	Lon     float64    `json:"lon"`
	PowerKW float64    `json:"power_kw"`
	Status  string     `json:"status"`
	Depot   *uuid.UUID `json:"depot_id,omitempty"`
}

func (s *Server) listChargers(w http.ResponseWriter, r *http.Request, p *Principal) {
	limit := intParam(r, "limit", 100, 1, 500)
	after := r.URL.Query().Get("after")
	afterID := uuid.Nil
	if after != "" {
		var err error
		if afterID, err = uuid.Parse(after); err != nil {
			writeProblem(w, 400, "invalid cursor")
			return
		}
	}
	var rows []chargerRow
	err := s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		// RLS exposes public chargers and the tenant's own depot chargers only
		rs, err := tx.Query(r.Context(), `SELECT id, name, tenant_id IS NULL, lat, lon, power_kw::float8, depot_id FROM charger WHERE id > $1 ORDER BY id LIMIT $2`, afterID, limit+1)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var c chargerRow
			if err := rs.Scan(&c.ID, &c.Name, &c.Public, &c.Lat, &c.Lon, &c.PowerKW, &c.Depot); err != nil {
				return err
			}
			rows = append(rows, c)
		}
		return rs.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID.String()
	}
	status, _ := s.cfg.Redis.HGetAll(r.Context(), chargerStatusHash).Result()
	for i := range rows {
		rows[i].Status = "AVAILABLE"
		if v, ok := status[rows[i].ID.String()]; ok {
			rows[i].Status = v
		}
		rows[i].Lat, rows[i].Lon = maskLocation(p, rows[i].Lat, rows[i].Lon) // charger sites are not personal data, but keep one rule
	}
	writeJSON(w, 200, map[string]any{"items": rows, "next": next})
}

// setChargerStatus publishes a status change on charger.status.v1; the stream workers' overlays pick it up within
// milliseconds. Tenants may change their own depot chargers; the public network is managed by the platform.
func (s *Server) setChargerStatus(w http.ResponseWriter, r *http.Request, p *Principal) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &body) {
		return
	}
	st, ok := map[string]chargerv1.ChargerStatus{"AVAILABLE": chargerv1.ChargerStatus_CHARGER_STATUS_AVAILABLE,
		"OUT_OF_SERVICE": chargerv1.ChargerStatus_CHARGER_STATUS_OUT_OF_SERVICE}[body.Status]
	if !ok {
		writeProblem(w, 400, "status must be AVAILABLE or OUT_OF_SERVICE")
		return
	}
	if s.cfg.Kafka == nil {
		writeProblem(w, 503, "status channel not configured")
		return
	}
	var own bool
	var kw float64
	err = s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT tenant_id = $2, power_kw::float8 FROM charger WHERE id = $1 AND tenant_id IS NOT NULL`, id, p.TenantID).Scan(&own, &kw)
	})
	if err == pgx.ErrNoRows || (err == nil && !own) {
		notFound(w) // public chargers and other tenants' chargers are not changeable here
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	ev := &chargerv1.ChargerStatusEvent{ChargerId: id.String(), Status: st, Ts: timestamppb.New(time.Now()), Seq: uint64(time.Now().UnixMilli()), PowerKwAvailable: float32(kw)}
	b, _ := proto.Marshal(ev)
	if err := s.cfg.Kafka.ProduceSync(r.Context(), &kgo.Record{Topic: kafkautil.ChargerStatus, Key: []byte(id.String()), Value: b}).FirstErr(); err != nil {
		serverError(w, err)
		return
	}
	_ = s.cfg.Redis.HSet(r.Context(), chargerStatusHash, id.String(), body.Status).Err()
	writeJSON(w, 202, map[string]any{"id": id, "status": body.Status})
}

// MirrorChargerStatus keeps the Redis status hash (read by the API) in step with charger.status.v1, which also
// carries the simulator's outages. It reads the whole compacted topic without a consumer group.
func MirrorChargerStatus(ctx context.Context, brokers []string, rdb redis.UniversalClient) error {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("vs-api-status"), kgo.ConsumeTopics(kafkautil.ChargerStatus),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		return err
	}
	defer cl.Close()
	for ctx.Err() == nil {
		fetches := cl.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return nil
		}
		pipe := rdb.Pipeline()
		n := 0
		fetches.EachRecord(func(r *kgo.Record) {
			var ev chargerv1.ChargerStatusEvent
			if proto.Unmarshal(r.Value, &ev) != nil {
				return
			}
			name := "AVAILABLE"
			switch ev.Status {
			case chargerv1.ChargerStatus_CHARGER_STATUS_OUT_OF_SERVICE:
				name = "OUT_OF_SERVICE"
			case chargerv1.ChargerStatus_CHARGER_STATUS_OCCUPIED:
				name = "OCCUPIED"
			}
			pipe.HSet(ctx, chargerStatusHash, ev.ChargerId, name)
			n++
		})
		if n > 0 {
			if _, err := pipe.Exec(ctx); err != nil && ctx.Err() == nil {
				return err
			}
		}
	}
	return nil
}
