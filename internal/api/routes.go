package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", s.ready)

	m.HandleFunc("GET /v1/me", s.guard("", "", "", s.me))

	m.HandleFunc("GET /v1/fleet/summary", s.guard(PermFleetRead, "fleet.summary", "fleet", s.fleetSummary))
	m.HandleFunc("GET /v1/vehicles", s.guard(PermFleetRead, "vehicle.list", "vehicle", s.listVehicles))
	m.HandleFunc("GET /v1/vehicles/{vin}", s.guard(PermFleetRead, "vehicle.read", "vehicle", s.getVehicle))
	m.HandleFunc("GET /v1/vehicles/{vin}/telemetry", s.guard(PermFleetRead, "vehicle.telemetry", "vehicle", s.vehicleTelemetry))
	m.HandleFunc("GET /v1/map/cells", s.guard(PermMapRead, "map.read", "map", s.mapCells))
	m.HandleFunc("GET /v1/map/vehicles", s.guard(PermMapRead, "map.vehicles", "map", s.mapVehicles))
	m.HandleFunc("GET /v1/map/chargers", s.guard(PermMapRead, "map.chargers", "map", s.mapChargers))

	m.HandleFunc("GET /v1/alerts", s.guard(PermAlertsRead, "alert.list", "alert", s.listAlerts))
	m.HandleFunc("GET /v1/alerts/stream", s.guard(PermAlertsRead, "alert.stream", "alert", s.alertStream))
	m.HandleFunc("GET /v1/alerts/{id}", s.guard(PermAlertsRead, "alert.read", "alert", s.getAlert))
	m.HandleFunc("POST /v1/alerts/{id}/ack", s.guard(PermAlertsWrite, "alert.ack", "alert", s.alertAction("acknowledge", "acknowledged")))
	m.HandleFunc("POST /v1/alerts/{id}/resolve", s.guard(PermAlertsWrite, "alert.resolve", "alert", s.alertAction("resolve", "resolved")))

	m.HandleFunc("GET /v1/chargers", s.guard(PermChargersRead, "charger.list", "charger", s.listChargers))
	m.HandleFunc("POST /v1/chargers/{id}/status", s.guard(PermChargersWrite, "charger.status", "charger", s.setChargerStatus))

	m.HandleFunc("GET /v1/plans", s.guard(PermPlansRead, "plan.list", "plan", s.listPlans))
	m.HandleFunc("POST /v1/plans:propose", s.guard(PermPlansWrite, "plan.propose", "plan", s.proposePlan))
	m.HandleFunc("POST /v1/plans/{id}/approve", s.guard(PermPlansWrite, "plan.approve", "plan", s.decidePlan("approved")))
	m.HandleFunc("POST /v1/plans/{id}/reject", s.guard(PermPlansWrite, "plan.reject", "plan", s.decidePlan("rejected")))
	m.HandleFunc("GET /v1/reports/cost", s.guard(PermReportsRead, "report.cost", "report", s.reportCost))
	m.HandleFunc("GET /v1/reports/energy", s.guard(PermReportsRead, "report.energy", "report", s.reportEnergy))
	m.HandleFunc("GET /v1/reports/soh", s.guard(PermReportsRead, "report.soh", "report", s.reportSoH))

	m.HandleFunc("GET /v1/audit", s.guard(PermAuditRead, "audit.read", "audit", s.listAudit))
	m.HandleFunc("POST /v1/privacy/erasure", s.guard(PermPrivacyWrite, "privacy.erasure.request", "erasure_request", s.requestErasure))
	m.HandleFunc("GET /v1/privacy/erasure/{id}", s.guard(PermPrivacyWrite, "privacy.erasure.read", "erasure_request", s.getErasure))
	m.HandleFunc("GET /v1/ops/pipeline", s.guard(PermOpsRead, "ops.read", "ops", s.opsPipeline))

	m.HandleFunc("POST /v1/copilot/messages", s.guard(PermCopilotUse, "", "copilot", func(w http.ResponseWriter, r *http.Request, p *Principal) {
		if s.copilot == nil {
			writeProblem(w, 503, "copilot not configured")
			return
		}
		s.copilot.Handle(w, r, p)
	}))
	m.HandleFunc("POST /v1/copilot/actions/{id}/approve", s.guard(PermCopilotUse, "", "copilot", func(w http.ResponseWriter, r *http.Request, p *Principal) {
		if s.copilot == nil {
			writeProblem(w, 503, "copilot not configured")
			return
		}
		s.copilot.Approve(w, r, p)
	}))
	m.HandleFunc("GET /v1/config", s.publicConfig)

	if s.cfg.StaticDir != "" {
		m.Handle("/", s.static())
	}
}

// publicConfig tells the web console where to authenticate (nothing secret).
func (s *Server) publicConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"issuer": os.Getenv("OIDC_PUBLIC_ISSUER"), "client_id": "voltsight-web"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	st := map[string]string{"postgres": "ok", "redis": "ok"}
	code := 200
	if err := s.cfg.Pool.Ping(r.Context()); err != nil {
		st["postgres"], code = "down", 503
	}
	if err := s.cfg.Redis.Ping(r.Context()).Err(); err != nil {
		st["redis"], code = "down", 503
	}
	if s.cfg.CH == nil {
		st["clickhouse"] = "not configured"
	} else if err := s.cfg.CH.Ping(r.Context()); err != nil {
		st["clickhouse"], code = "down", 503
	} else {
		st["clickhouse"] = "ok"
	}
	writeJSON(w, code, st)
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, p *Principal) {
	var perms []string
	seen := map[string]bool{}
	for _, r := range p.Roles {
		for _, x := range rolePerms[r] {
			if !seen[x] {
				seen[x] = true
				perms = append(perms, x)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"user_id": p.UserID, "tenant_id": p.TenantID, "roles": p.Roles, "permissions": perms})
}

// static serves the web console; unknown paths fall back to index.html (client-side routing).
func (s *Server) static() http.Handler {
	fs := http.FileServer(http.Dir(s.cfg.StaticDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			notFound(w)
			return
		}
		p := filepath.Join(s.cfg.StaticDir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			if r.URL.Path != "/" {
				r.URL.Path = "/"
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		fs.ServeHTTP(w, r)
	})
}
