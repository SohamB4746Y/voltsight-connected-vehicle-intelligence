# Submission manifest

Structure follows the organisers' e-mail “Hackathon Submission – Instructions”: **1 – Solution Document, 2 – Hackathon Explainer Video, 3 – Technical Artifacts**, then the Google Form.

Repository: https://github.com/SohamB4746Y/voltsight-connected-vehicle-intelligence (branch `main`; the final documentation is on branch `claude/jolly-clarke-65flh3` until merged).
Live demo (GitHub Codespaces, a demo environment, not a production cloud; may be stopped; sign-in passwords are generated per deployment and are **not** in this package): https://special-acorn-jj4x7q5rx5jxfqj67-8080.app.github.dev — to make the map render, re-run `deploy/live/up.sh` in the Codespace after the CI image job has published the current `main` image.

## Google Drive layout (upload exactly this)

```
VoltSight_Submission/
├── 01_Solution Document/        VoltSight_Solution_Document.pdf
├── 02_Hackathon Explainer Video/ VoltSight_Hackathon_Explainer.mp4
└── 03_Technical Artifacts/      (contents of 03_Technical_Artifacts/ below; or VoltSight_Final_Submission.zip)
```

## 1 — Solution Document
| File | Purpose | Format | Location | Status |
|---|---|---|---|---|
| VoltSight_Solution_Document.pdf | Detailed Solution Document (19 pages: judge path, architecture, data, algorithms, ML, Copilot, security, STRIDE, performance, testing, deployment, matrix, limitations). Built without the organisers' template (not available to the author) | PDF | `01_Solution_Document/` | Complete; limits in DOCUMENT_QA_REPORT.md |

## 2 — Hackathon Explainer Video
| File | Purpose | Format | Location | Status |
|---|---|---|---|---|
| VoltSight_Hackathon_Explainer.mp4 | 4:48 explainer: real console recording + slides + synthetic narration | MP4 1280×720 | `02_Hackathon_Explainer_Video/` | Complete; limits in VIDEO_QA_REPORT.md (no basemap shown; narration is TTS) |

## 3 — Technical Artifacts (`03_Technical_Artifacts/`)
| Folder | Contents |
|---|---|
| 00_Codebase | Source snapshot of the repository at the time of packaging (also on GitHub) |
| 01_Architecture | architecture.md, CAP/PACELC, deployment.md, standalone `architecture_diagram.svg` |
| 02_ER_Diagram | ER diagram (Mermaid source + README), 3NF document |
| 03_ADRs | ADR-001 … ADR-010 |
| 04_Requirement_Matrix | machine-checked matrix, judge view, submission status, code-freeze note, `reqtrace` gate source |
| 05_Performance | capacity analysis; evidence G4 (100K run), G14 (burst, alert latency, PG vs ClickHouse), G9b (API latency) |
| 06_Testing | testing summary, chaos results, G1, G13, 31 live checks, web auth tests |
| 07_Security | security summary, OWASP map, compliance, ZAP report, SBOM, scans |
| 08_STRIDE | STRIDE threat model |
| 09_SQL_Optimization | write-up, real EXPLAIN ANALYZE plans, benchmark SQL |
| 10_Algorithms | algorithms and complexity with measurements |
| 11_ML | ML results, evidence JSON, training script |
| 12_Agentic_AI | agent architecture, G10 scenarios, ADR-005, Copilot source |
| 13_Compliance | compliance mapping, code-freeze record |
| 14_DevOps | Helm, Terraform, Compose, live bring-up, Dockerfile, GitHub workflows, validation output |
| 15_Screenshots | 14 real screenshots + capture notes |
| 16_Deployment | live deployment guide, evidence, demo scripts |
| 17_AI_OSS_Declaration | AI / open-source / data disclosure, SBOM |

## Reports at the package root
SUBMISSION_MANIFEST.md · FINAL_REQUIREMENT_MATRIX.md (60 requirements: PASS 28, PARTIAL 28, BLOCKED 2, FAIL 2) · DOCUMENT_QA_REPORT.md · VIDEO_QA_REPORT.md · FINAL_HONESTY_AUDIT.md.

## Google Form
https://forms.gle/f51LCW2H1T7CvbeUA — upload the Drive folder link there. (Only the owner can submit the form.)

## Git state at packaging
See the final report; tag `v1.0-submission` exists locally on an earlier commit (`331b4b4`) and was not moved.
