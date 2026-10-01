# Live deployment evidence

Everything here was produced by running the live profile (`deploy/live/up.sh`) on the build sandbox and reaching it at
`http://localhost:8080` through the Caddy edge. **None of it is from a public internet deployment; none exists yet.**

| File | What it is |
|---|---|
| `live_checks.json` | 31 scripted checks as real users (sign-in, RBAC, tenant isolation, CORS, exposure, Copilot, API latency); produced by `tests/e2e/live_checks.mjs` |
| `shots/` | screenshots from the Playwright browser journey (`tests/e2e/smoke.mjs`) |
| `health.txt` | container health, `/readyz`, admin endpoints, Prometheus targets, image id, timestamp |
| `restart-recovery.txt`, `full-restart.txt` | single-service restarts and the full stop/start (includes the defect found and its fix) |
| `alert-latency-recv-to-detect.txt` | receive-to-detect latency from the alert rows (not end to end) and the event rate |
| `resource-usage.txt` | container memory/CPU snapshot |
| `image-scan.txt` | Trivy 0.57.1 on the built image |
| `helm-terraform-validation.txt` | helm lint, kubeconform, terraform fmt/validate |
| `secrets-scan.txt` | gitleaks over history and tree |

Not produced (and not claimed): a public URL, a real Kubernetes cluster run, 100K events/s on this profile, end-to-end
alert latency on this profile. See `docs/deployment-live.md` section 10.
