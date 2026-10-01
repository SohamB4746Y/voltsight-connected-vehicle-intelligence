// Command vsbench measures API latency under concurrent load (a mixed read workload) and prints percentiles.
// It signs in once with the dev test client (direct grant) and replays a fixed mix of endpoints.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"
	"time"

	"voltsight/internal/dotenv"
)

func main() {
	var (
		base    = flag.String("base", "http://127.0.0.1:8081", "API base URL")
		kc      = flag.String("keycloak", "http://localhost:8080", "Keycloak base URL")
		user    = flag.String("user", "dispatcher@meridian.example", "demo user")
		conc    = flag.Int("c", 16, "concurrent clients")
		dur     = flag.Duration("d", 20*time.Second, "duration")
		outPath = flag.String("out", "", "write JSON result here")
	)
	flag.Parse()
	env, _ := dotenv.Load(".env")
	resp, err := http.PostForm(*kc+"/realms/voltsight/protocol/openid-connect/token", url.Values{"grant_type": {"password"}, "client_id": {"voltsight-test"},
		"client_secret": {dotenv.Get(env, "KC_TEST_CLIENT_SECRET")}, "username": {*user}, "password": {dotenv.Get(env, "DEMO_USER_PASSWORD")}})
	must(err)
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	must(json.NewDecoder(resp.Body).Decode(&tr))
	resp.Body.Close()
	if tr.AccessToken == "" {
		must(fmt.Errorf("no token"))
	}
	// a pool of real VINs to read
	get := func(p string) []byte {
		req, _ := http.NewRequest("GET", *base+p, nil)
		req.Header.Set("Authorization", "Bearer "+tr.AccessToken)
		r, err := http.DefaultClient.Do(req)
		must(err)
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return b
	}
	var vl struct {
		Items []struct{ VIN string } `json:"items"`
	}
	_ = json.Unmarshal(get("/v1/vehicles?limit=200"), &vl)
	paths := []string{"/v1/fleet/summary", "/v1/vehicles?limit=50", "/v1/alerts?limit=50", "/v1/alerts?status=open&limit=50", "/v1/chargers?limit=100", "/v1/map/cells?cell=0.01"}
	for _, v := range vl.Items {
		paths = append(paths, "/v1/vehicles/"+v.VIN)
	}
	type sample struct {
		path string
		d    time.Duration
		code int
	}
	var mu sync.Mutex
	var all []sample
	var wg sync.WaitGroup
	stop := time.Now().Add(*dur)
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cl := &http.Client{Timeout: 10 * time.Second}
			for n := i; time.Now().Before(stop); n++ {
				p := paths[n%len(paths)]
				req, _ := http.NewRequest("GET", *base+p, nil)
				req.Header.Set("Authorization", "Bearer "+tr.AccessToken)
				t0 := time.Now()
				r, err := cl.Do(req)
				code := 0
				if err == nil {
					_, _ = io.Copy(io.Discard, r.Body)
					r.Body.Close()
					code = r.StatusCode
				}
				d := time.Since(t0)
				mu.Lock()
				all = append(all, sample{p, d, code})
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	sort.Slice(all, func(i, j int) bool { return all[i].d < all[j].d })
	pct := func(p float64) float64 { return float64(all[int(p*float64(len(all)-1))].d.Microseconds()) / 1000 }
	bad := 0
	for _, s := range all {
		if s.code != 200 && s.code != 429 {
			bad++
		}
	}
	res := map[string]any{"requests": len(all), "concurrency": *conc, "duration_s": dur.Seconds(), "rps": float64(len(all)) / dur.Seconds(),
		"p50_ms": pct(0.5), "p95_ms": pct(0.95), "p99_ms": pct(0.99), "max_ms": float64(all[len(all)-1].d.Microseconds()) / 1000, "errors_non_200_429": bad}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	if *outPath != "" {
		_ = os.WriteFile(*outPath, append(b, '\n'), 0o644)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsbench:", err)
		os.Exit(1)
	}
}
