# Gate G10 status (Copilot and vector search) — PASS with the deterministic provider; the Claude provider was NOT exercised (no API key)
Run: `go test -tags stack -count=1 ./internal/copilot` (8 scenarios, all pass) and `go test ./internal/embed`.
| Criterion | Test / result |
|---|---|
| grounded answer cites ids that exist in tool results; each step audited (agent audit rows == tool calls) | `TestGroundedAnswersCiteEvidenceAndEveryStepIsAudited` |
| no evidence ⇒ "insufficient evidence", no citations | `TestNoEvidenceMeansInsufficientEvidence` |
| cross-tenant VIN indistinguishable from unknown | `TestCrossTenantVehicleIsIndistinguishableFromUnknown` |
| unknown tool, extra/malformed arguments, unauthorised write, oversized query rejected | `TestGuardrailsRejectUnknownMalformedAndUnauthorisedCalls` |
| prompt injection inside a retrieved incident executes nothing | `TestPromptInjectionInRetrievedTextExecutesNothing` |
| writes are two-phase: agent only registers; dispatcher cannot approve; energy manager approves once; cross-tenant approval impossible | `TestWritesNeedHumanApproval` |
| LLM down ⇒ graceful message (dashboards unaffected) | `TestLLMOutageDegradesGracefully` |
| input validation | `TestMessageValidation` |
| vector search quality | recall@5 **0.958** on a 24-query labelled paraphrase set (lexical hashing embedder; the corpus is small and written by the same author — treat as a smoke metric) |
**Not done:** vector-store-down degradation test; recorded real-LLM run.
