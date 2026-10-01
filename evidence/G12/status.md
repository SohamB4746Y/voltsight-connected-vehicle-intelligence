# Gate G12 status (security scans) - PARTIAL (scans done; compliance suites listed under "Tests")

| Scan | Tool | Result | Evidence |
|---|---|---|---|
| Secrets in git history (18 commits) | gitleaks 8.21.2 | **no leaks** | `gitleaks.json` |
| Go dependency vulnerabilities (reachable) | govulncheck (Go 1.27.0) | **No vulnerabilities found** | `govulncheck.txt` |
| JS dependencies | npm audit --omit=dev | **0 vulnerabilities** | (console output) |
| SAST | semgrep p/golang + p/owasp-top-ten on cmd, internal, web/src | 1 finding (`math/rand` retry jitter in the sink) - waived inline with a justification, not security-sensitive | `semgrep.json` |
| Filesystem / IaC | Trivy 0.57.1 (HIGH, CRITICAL; vuln + secret + misconfig) | go.mod findings fixed by upgrading `golang.org/x/crypto` and `github.com/moby/go-archive` (both indirect, test-time only); 2 GCP Terraform findings waived in `.trivyignore` with reasons (the checks look for a deprecated argument / do not follow the node-pool block) | `trivy-fs.json` |
| Container image | Trivy image scan | **NOT RUN** - the image build was not executed in this sandbox |  |
| DAST | OWASP ZAP | **NOT RUN** |  |
| TLS 1.3 of the API edge | `openssl s_client` against `vsapi -tls` | **TLS 1.3 accepted, TLS 1.2 refused** (`tls_api.txt`); no full scanner (testssl/nmap) run. The default local listener is plain HTTP; the gateway enforces TLS 1.3 + mTLS (G3 handshake tests); the Helm ingress pins TLSv1.3 |
| Encryption at rest | - | local volumes are NOT encrypted: LIMITATION. Terraform enables KMS/CMEK for RDS/Cloud SQL, S3/GCS, ElastiCache/Memorystore, MSK, EKS secrets (validated, not applied) | `deploy/terraform/*` |
