//go:build stack

package alerts

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	alertv1 "voltsight/gen/voltsight/alert/v1"
	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/testkit"
)

func pgAs(t *testing.T, env map[string]string, role, pwKey string) *pgxpool.Pool {
	dsn, err := dbtool.OwnerDSN(env)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword(role, dotenv.Get(env, pwKey))
	pool, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// G6.8: the same alert delivered many times is stored once; it is fanned out; only its own tenant can read it.
func TestAlertServiceIsIdempotentAndTenantIsolated(t *testing.T) {
	env, err := dotenv.Load("../../.env")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := dbtool.OwnerDSN(env)
	if err != nil {
		t.Fatal(err)
	}
	op, err := pgxpool.New(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	var tenant, vin, otherTenant string
	if err := op.QueryRow(context.Background(), `SELECT tenant_id::text, vin FROM vehicle LIMIT 1`).Scan(&tenant, &vin); err != nil {
		t.Skipf("database not seeded (make seed): %v", err)
	}
	if err := op.QueryRow(context.Background(), `SELECT id::text FROM tenant WHERE id::text <> $1 LIMIT 1`, tenant).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	win := time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC).Add(time.Duration(time.Now().UnixNano()%1_000_000) * time.Hour) // unique per run
	t.Cleanup(func() { _, _ = op.Exec(context.Background(), `DELETE FROM alert WHERE vin = $1 AND window_start = $2`, vin, win) })

	ev, _ := structpb.NewStruct(map[string]any{"estimator": "t", "distance_to_charger_km": 12.5})
	a := &alertv1.AlertEvent{TenantId: tenant, Vin: vin, Rule: alertv1.Rule_RULE_RANGE_CRITICAL, Severity: alertv1.Severity_SEVERITY_CRITICAL,
		WindowStart: timestamppb.New(win), DetectedAt: timestamppb.Now(), SourceEventTs: timestamppb.Now(), SourceRecvTs: timestamppb.Now(),
		SocPct: 7, MarginKm: -3.2, NearestChargerId: "c1", Evidence: ev}
	b, _ := proto.Marshal(a)

	topic := testkit.Topic(t, "t6.alerts", 3)
	cl, err := kgo.NewClient(kgo.SeedBrokers(testkit.Brokers...))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	for i := 0; i < 5; i++ { // delivered five times (retries, replays)
		if err := cl.ProduceSync(context.Background(), &kgo.Record{Topic: topic, Key: []byte(tenant + "|" + vin), Value: b}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	// plus garbage, which must be counted and skipped, not fatal
	if err := cl.ProduceSync(context.Background(), &kgo.Record{Topic: topic, Value: []byte("not protobuf \xff\xff")}).FirstErr(); err != nil {
		t.Fatal(err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Password: dotenv.Get(env, "REDIS_PASSWORD")})
	defer rdb.Close()
	sub := rdb.Subscribe(context.Background(), Channel(tenant))
	defer sub.Close()
	if _, err := sub.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}

	svc, err := New(Config{Brokers: testkit.Brokers, Group: "ga-" + uuid.NewString()[:8], Topic: topic}, pgAs(t, env, "voltsight_alerts", "DB_ALERTS_PASSWORD"), rdb)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for svc.Stats.Consumed.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var n int
	if err := op.QueryRow(context.Background(), `SELECT count(*) FROM alert WHERE vin = $1 AND window_start = $2`, vin, win).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows for one alert delivered 5 times, want 1 (err %v)", n, err)
	}
	if svc.Stats.Inserted.Load() != 1 || svc.Stats.Duplicates.Load() != 4 || svc.Stats.DecodeErrors.Load() != 1 {
		t.Fatalf("inserted=%d duplicates=%d decodeErrors=%d, want 1/4/1", svc.Stats.Inserted.Load(), svc.Stats.Duplicates.Load(), svc.Stats.DecodeErrors.Load())
	}
	msg, err := sub.ReceiveMessage(context.Background())
	if err != nil || msg.Channel != Channel(tenant) {
		t.Fatalf("no pub/sub message: %v", err)
	}

	// the application role sees the alert only with its own tenant context
	app := pgAs(t, env, "voltsight_app", "DB_APP_PASSWORD")
	count := func(tn string) int {
		tx, err := app.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background()) //nolint:errcheck
		if _, err := tx.Exec(context.Background(), `SELECT set_config('app.tenant_id', $1, true)`, tn); err != nil {
			t.Fatal(err)
		}
		var c int
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM alert WHERE vin = $1`, vin).Scan(&c); err != nil && err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		return c
	}
	if got := count(tenant); got < 1 {
		t.Fatalf("owning tenant sees %d alerts", got)
	}
	if got := count(otherTenant); got != 0 {
		t.Fatalf("another tenant sees %d of this tenant's alerts", got)
	}
}
