# G0 static checks and gate status

Commit under test: see `evidence/G0/20261001T090257Z-602191d/` for the cold-start run (env.json, results.json, run.log).

## G0.8 static checks — run locally 2026-10-01 against commit 602191d (+ this evidence file)
| Check | Command | Result |
|---|---|---|
| Secrets in git history | `gitleaks detect --redact` | 3 commits scanned, no leaks found |
| Compose validity | `docker compose ... config -q` | exit 0 |
| YAML lint | `yamllint -s -c .yamllint.yml deploy .github` | exit 0 |
| Misconfig/secret scan | `trivy fs --scanners misconfig,secret --severity HIGH,CRITICAL` | no secret findings; **misconfig scanner had no files to scan (no Dockerfiles/IaC yet) — result is vacuous until M1+** |

## Gate status
| Criterion | Status |
|---|---|
| G0.1 tools | PASS |
| G0.2 engine memory/CPU | PASS (11.49 GB, 14 CPUs) |
| G0.3 host baseline | PASS (disk free 59.1 GB at run time — bounds the "xl" dataset tier) |
| G0.4 clean cold start healthy | PASS (85.5 s from `down -v`; an earlier run, 163 s, failed on the objectstore healthcheck and was fixed in 602191d) |
| G0.5 functional probes | PASS |
| G0.6 idle memory | PASS (measured 1.8 GB total idle) |
| G0.7 OTLP -> Prometheus | PASS |
| G0.8 static checks | PASS locally (see caveat above) |
| G0.9 CI green on GitHub | **BLOCKED** — no GitHub remote exists yet; creating a repository is an outward-facing action awaiting the owner's go-ahead |
| G0.10 no fixed secrets | PASS |

**Overall G0: PARTIAL** (9/10; G0.9 blocked on a GitHub repository).
