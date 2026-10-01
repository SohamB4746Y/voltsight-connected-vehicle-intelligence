# Gate G3 status: ingest gateway and Kafka

Criteria: [`docs/gates/G3.md`](../../docs/gates/G3.md). Targeted tests were run against the **real** compose stack (Vault, PostgreSQL with the 100K seed, Kafka); the full regression pass is deferred to the final verification phase. Test names are in `internal/normalise`, `internal/ingest` (`-tags stack`), `internal/kafkautil`.

| Criterion | Status | Evidence |
|---|---|---|
| G3.1 DTC / VIN parsing | PASS | all 4 x 4 x 4096 = 65,536 valid OBD-II codes round-trip raw <-> text; known J1979 vectors; ragged/non-hex rejected; VIN covered since G1 |
| G3.2 dialects normalise identically | PASS | OEM A v1, A v2 (speed renamed, soh_hint added) and B produce the same event for the same observation; 15 rejection cases map to the right DLQ class; `Validate` covers 18 bad-field cases; 97.3% coverage |
| G3.3 mTLS | PASS | real Vault: no certificate, unrelated CA with a valid identity, unregistered, registration/identity mismatch, revoked in the CRL only, revoked in the DB only, expired, TLS 1.2 are all refused at the handshake; the valid connector works before, between and after; responses are TLS 1.3 |
| G3.4 tenant from the certificate | PASS | a forged `tenant_id` and `recv_ts` in the payload are overwritten on the Kafka record; another tenant's VIN lands in the DLQ as `UNAUTHORIZED_VIN` |
| G3.5 per-event isolation | PASS | one batch with 7 events (1 valid, 1 forged-tenant, foreign VIN, bad check digit, missing schema, too old, corrupt bytes): 2 accepted, 5 DLQ'd with 5 distinct classes, original bytes preserved; a broken envelope is a 400 |
| G3.6 simulator end to end | PASS | 100,000 vehicles, 8 simulated s, 2% duplicates / 3% out-of-order / 0.5% corrupted / mid-run schema v2 / burst: `accepted + rejected == sent`; Kafka offsets equal the gateway's counts exactly (telemetry and DLQ); duplicates and out-of-order events pass through; OEM-JSON run had **0** `SCHEMA_INVALID` rejects across the v2 rollout; both encodings |
| G3.7 back-pressure | PASS | 10 concurrent 800-event batches against a 150 KB in-flight budget: **106 refusals (429), all with `Retry-After`**; every batch eventually accepted; the topic holds exactly the accepted events (a refused request produces nothing); per-connector rate limit refuses an over-burst batch with 429 and produces nothing |
| G3.8 topics | PASS | `telemetry.v1` 64 partitions / 72 h, `telemetry.dlq.v1` 12 / 14 d, `charger.status.v1` compacted, `alerts.v1`, `privacy.erasure.v1` exist as designed (`make bootstrap` is idempotent); 20 VINs x 5 events each always on one partition, spread over >= 3 |
| G3.9 gateway throughput | MEASURED | see below |

## G3.9 measurement (real binaries, this laptop, single Kafka broker, single gateway process)
`vsgateway` + `vssim -vehicles 100000 -duration 30 -start-tod 27000 -format proto -gateway ... -dup-rate 0.02 -ooo-rate 0.03 -fault-rate 0.002` (`gateway_throughput_proto.json`):

| Events through the mTLS gateway into Kafka (acks=all) | Wall | Rate | 429 / 503 / failed |
|---|---|---|---|
| 2,976,256 (2,970,477 accepted, 5,779 to the DLQ) | 9.13 s | **326K events/s** end to end | 0 / 0 / 0 |

Kafka-side check after the run: `telemetry.v1` end offsets sum **2,970,477**, `telemetry.dlq.v1` **5,779**: identical to the gateway's accepted/rejected counters. In-process test runs measured 248K events/s (OEM JSON) and 443K events/s (protobuf) for 0.98M events.

## Caveats (stated, not hidden)
- These are **short runs** (4-9 s of traffic) with the simulator and the gateway sharing one machine; they show the gateway path is not the bottleneck at 100K events/s, they are **not** a sustained-load result. The sustained 100K events/s, 3x-burst-for-5-minutes and soak tests are gate G14.
- Single broker, replication factor 1, `min.insync.replicas=1` in the `core` profile: durability under broker failure is tested with the 3-broker profile at G13.
- At-least-once: a 503 makes the client resend the whole batch, producing duplicates that the stream worker de-duplicates (M4). Exactly-once to Kafka is not claimed.
- OEM dialect parsers are compiled in; onboarding a new OEM needs a gateway release (the PDF's "without downtime" is satisfied by rolling update, not by a hot-reloadable registry). Schema-version rollout *within* an OEM (v1 to v2) is seamless and tested.
- The gateway checks tenant ownership of the VIN but not that the vehicle's OEM matches the connector's OEM (would need a `vehicle_model` grant for the gateway role).
- The gateway's server certificate is issued by Vault at start-up (development convenience); production loads it from a mounted secret (Helm, M12).
- The sender-side `Sender` counts "Sent" per event including simulator duplicates, so `accepted + rejected == sent` holds only if nothing is lost between sender and gateway.
