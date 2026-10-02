# Document QA report: VoltSight_Solution_Document.pdf

Checked 2026-10-02 on the final PDF (19 pages, A4, built from HTML with headless Chromium; generator `build.py` input = repository evidence files).

| Check | Result |
|---|---|
| Page count / page numbers | 19 pages; header and footer with “Page n / 19” on every page; cover shows no blank pages |
| Table of contents | Present (page 3); page numbers are computed from the rendered PDF by searching section headings, then rebuilt (two-pass) |
| Pages rendered to images and inspected | All pages rendered at 50–90 dpi and viewed as contact sheets; the architecture, performance, ML, security and screenshot pages were additionally viewed at reading size |
| Defects found and fixed during QA | Half-empty pages from forced page breaks (fixed); unreadable auto-laid-out architecture diagram replaced by a hand-built layered SVG; unreadable one-row Copilot/security/workflow flowcharts replaced by step strips; performance chart text overlap (rebuilt); duplicate figure numbers (renumbered); algorithm table contained detection-quality rows (filtered); alert-latency explanation aligned with evidence/G14 wording |
| Broken / clipped images or tables | None seen in the inspected renders |
| Secrets / credentials | Text of the PDF searched for password, secret, token, api_key, credential patterns: only prose uses of the words; no password, token or key value; screenshots were taken after sign-in and show no secrets |
| Numbers consistent with evidence | 98.3K ev/s, 6.0M events, 18.0M burst events, p50 9.8 s / p99 17.0 s, API 16.9 / 26.8 / 41.0 ms, ML metrics, ZAP 58/0/3, matrix counts (computed by the script from the matrix file) all trace to the files cited in the text |
| Requirement matrix counts | Computed from `docs/compliance/FINAL_REQUIREMENT_MATRIX.md` at build time: PASS 28, PARTIAL 28, BLOCKED 2, FAIL 2 |
| References to draft state | None |

## Known limitations of the document (not defects fixed)
- No automated spell-check was run; text was reviewed by reading.
- The ER figure is the simplified core model rendered by Mermaid; text is small at A4 width (the full generated ER diagram is in the technical artifacts).
- Observability shows Prometheus only; no Grafana or log screenshot is included. No GitHub Actions UI screenshot is included (private repository, login needed): CI is documented as a table with run numbers from the GitHub API.
- The map screenshots show the markers-only fallback because the OpenFreeMap host was unreachable from the capture environment; this is stated in the captions and in Section 26.
- Screenshots were captured on 2026-10-02 from the live profile running in the build environment (same Compose profile as the Codespaces demo), not from the owner's Codespaces URL, whose sign-in credentials were not available to the author.
- The Solution Document template referred to in the organisers' e-mail was not available; the structure follows the problem statement and the requested outline.
