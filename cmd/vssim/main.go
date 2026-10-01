// Command vssim runs the fleet simulator. Without a delivery sink it discards messages (generator
// benchmark); -out writes framed messages to a file. Delivery to the ingest gateway is added in M3.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/ingest"
	"voltsight/internal/pki"
	"voltsight/internal/sim"
)

func main() {
	var (
		cfg          sim.Config
		format       = flag.String("format", "proto", "proto | oem-json")
		out          = flag.String("out", "", "write length-prefixed messages to this file instead of discarding them")
		report       = flag.String("report", "", "write the run report JSON here")
		realtime     = flag.Bool("realtime", false, "pace simulated time to the wall clock")
		gateway      = flag.String("gateway", "", "deliver to this ingest gateway over mTLS, e.g. https://127.0.0.1:8443 (issues and registers connector certificates via Vault)")
		kafkaBrokers = flag.String("brokers", "127.0.0.1:29092", "Kafka brokers for charger-status events (with -gateway)")
	)
	flag.Uint64Var(&cfg.Seed, "seed", 1, "behaviour seed")
	flag.IntVar(&cfg.Vehicles, "vehicles", 100_000, "number of vehicles")
	flag.IntVar(&cfg.Duration, "duration", 60, "simulated seconds")
	flag.IntVar(&cfg.Shards, "shards", 0, "worker shards (default GOMAXPROCS)")
	flag.IntVar(&cfg.StartTOD, "start-tod", 0, "local time of day at start, seconds after midnight (default 19800 = 05:30)")
	flag.Float64Var(&cfg.Speedup, "speedup", 1, "simulated seconds per wall second (with -realtime)")
	flag.Float64Var(&cfg.DupRate, "dup-rate", 0, "probability an event is delivered twice")
	flag.Float64Var(&cfg.OOORate, "ooo-rate", 0, "probability an event is delayed (out of order)")
	flag.Float64Var(&cfg.MalformedRate, "fault-rate", 0, "probability a payload is corrupted")
	flag.IntVar(&cfg.BurstAt, "burst-at", 0, "sample-rate burst start (sim s)")
	flag.IntVar(&cfg.BurstDuration, "burst-duration", 0, "burst length (sim s)")
	flag.IntVar(&cfg.BurstMult, "burst-mult", 1, "samples per second per vehicle during the burst")
	flag.IntVar(&cfg.OutageAt, "outage-at", 0, "connectivity outage start (sim s)")
	flag.IntVar(&cfg.OutageDuration, "outage-duration", 0, "outage length (sim s)")
	flag.Float64Var(&cfg.OutageFraction, "outage-fraction", 0, "share of vehicles buffering during the outage")
	flag.IntVar(&cfg.SchemaV2At, "schema-v2-at", 0, "sim second at which OEM-A vehicles switch to schema v2")
	flag.IntVar(&cfg.ChargerOutageAt, "charger-outage-at", 0, "charger outage start (sim s)")
	flag.IntVar(&cfg.ChargerOutageDuration, "charger-outage-duration", 0, "charger outage length (sim s)")
	flag.Float64Var(&cfg.ChargerOutageFraction, "charger-outage-fraction", 0, "share of chargers taken out of service")
	flag.Float64Var(&cfg.LowSoCStartFraction, "low-soc-fraction", 0, "share of vehicles that start with a low battery (default 0.08)")
	flag.StringVar(&cfg.TruthDir, "truth-dir", "", "write ground-truth files here")
	flag.Parse()
	cfg.Format = sim.Format(*format)
	cfg.RealTime = *realtime

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var sink sim.Sink
	null := &sim.NullSink{}
	sink = null
	var sender *ingest.Sender
	if *gateway != "" {
		sender = connectToGateway(ctx, &cfg, *gateway, *kafkaBrokers)
		sink = sender
	}
	var fsink *fileSink
	if *out != "" {
		var err error
		if fsink, err = newFileSink(*out); err != nil {
			fatal(err)
		}
		sink = fsink
	}

	var stats sim.Stats
	var msBefore runtime.MemStats
	runtime.ReadMemStats(&msBefore)
	start := time.Now()
	err := sim.Run(ctx, cfg, sink, &stats)
	wall := time.Since(start)
	if fsink != nil {
		if cerr := fsink.close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if err != nil {
		fatal(err)
	}
	snap := stats.Snapshot()
	rep := map[string]any{
		"vehicles": cfg.Vehicles, "sim_seconds": cfg.Duration, "format": *format, "shards": cfg.Shards,
		"gomaxprocs": runtime.GOMAXPROCS(0), "wall_seconds": wall.Seconds(),
		"events_generated": snap.Generated, "events_emitted": snap.Emitted,
		"events_per_wall_second": float64(snap.Emitted) / wall.Seconds(),
		"bytes":                  snap.Bytes, "avg_bytes_per_event": float64(snap.Bytes) / float64(max64(snap.Emitted, 1)),
		"mb_per_wall_second": float64(snap.Bytes) / 1e6 / wall.Seconds(),
		"duplicates":         snap.Duplicates, "out_of_order": snap.OutOfOrder, "malformed": snap.Malformed,
		"outage_buffered": snap.OutageBuffered, "outage_flushed": snap.OutageFlushed,
		"charger_events": snap.ChargerEvents, "strandings": snap.Strandings, "trips": snap.Trips,
		"charge_sessions": snap.ChargeSessions, "peak_tick_messages": snap.PeakTickMessages,
		"heap_alloc_mb": float64(ms.HeapAlloc) / 1e6, "sys_mb": float64(ms.Sys) / 1e6,
		"delivery_digest": fmt.Sprintf("%016x", null.Digest()),
	}
	if sender != nil {
		rep["gateway"] = map[string]any{
			"url": *gateway, "requests": sender.Stats.Requests.Load(), "events_sent": sender.Stats.Sent.Load(),
			"accepted": sender.Stats.Accepted.Load(), "rejected": sender.Stats.Rejected.Load(),
			"retried_429": sender.Stats.Retried429.Load(), "retried_503": sender.Stats.Retried503.Load(),
			"failed": sender.Stats.Failed.Load(), "bytes_sent": sender.Stats.BytesSent.Load(),
			"charger_events_published":     sender.Stats.ChargerEvents.Load(),
			"end_to_end_events_per_second": float64(sender.Stats.Sent.Load()) / wall.Seconds(),
		}
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
	if *report != "" {
		if err := os.WriteFile(*report, append(b, '\n'), 0o644); err != nil {
			fatal(err)
		}
	}
}

// connectToGateway provisions one Vault-issued, registered certificate per connector and returns a sender.
// Development tooling: it uses the database owner connection to register the credentials.
func connectToGateway(ctx context.Context, cfg *sim.Config, url, brokers string) *ingest.Sender {
	env, err := dotenv.Load(".env")
	if err != nil {
		fatal(err)
	}
	addr := dotenv.Get(env, "VAULT_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8200"
	}
	vault := pki.NewVault(addr, dotenv.Get(env, "VAULT_DEV_TOKEN"))
	dsn, err := dbtool.OwnerDSN(env)
	if err != nil {
		fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	keys, err := sim.Connectors(*cfg)
	if err != nil {
		fatal(err)
	}
	certs, err := ingest.Provision(ctx, vault, pool, keys, 24*time.Hour)
	if err != nil {
		fatal(err)
	}
	roots, err := vault.RootPool(ctx)
	if err != nil {
		fatal(err)
	}
	s := &ingest.Sender{URL: url, Clients: map[sim.ConnectorKey]*http.Client{}}
	for k, c := range certs {
		s.Clients[k] = ingest.NewClient(c, roots, "localhost")
	}
	if s.Kafka, err = kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...)); err != nil {
		fatal(err)
	}
	cfg.TimeBase = time.Now() // current event times so the gateway's freshness checks pass
	fmt.Fprintf(os.Stderr, "vssim: %d connector certificates issued and registered; waiting 12 s for the gateway to refresh its directory\n", len(keys))
	time.Sleep(12 * time.Second)
	return s
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "vssim:", err)
	os.Exit(1)
}

// fileSink writes 4-byte big-endian length-prefixed payloads (tab-free, binary-safe).
type fileSink struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

func newFileSink(path string) (*fileSink, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &fileSink{f: f, w: bufio.NewWriterSize(f, 1<<20)}, nil
}

func (s *fileSink) Emit(_ int, _ int, batch []sim.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hdr [4]byte
	for i := range batch {
		binary.BigEndian.PutUint32(hdr[:], uint32(len(batch[i].Payload)))
		if _, err := s.w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := s.w.Write(batch[i].Payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *fileSink) close() error {
	if err := s.w.Flush(); err != nil {
		return err
	}
	return s.f.Close()
}
