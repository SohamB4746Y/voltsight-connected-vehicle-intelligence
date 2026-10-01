-- G9b: SQL optimisation experiments. Run as the table owner against the seeded database; everything it creates is
-- removed at the end. Each experiment prints the real EXPLAIN (ANALYZE, BUFFERS) plan BEFORE and AFTER the change.
\set ON_ERROR_STOP on
\pset pager off
\timing off
SET max_parallel_workers_per_gather = 0;   -- single-threaded plans: easier to compare
SELECT set_config('app.bench_tenant', (SELECT id::text FROM tenant WHERE name = 'Meridian Logistics'), false);

-- ---- data volume: 1.5M alerts, 1.5M trips, 600K audit rows for one tenant (bench rows are tagged) ----
INSERT INTO alert (tenant_id, vin, rule, severity, window_start, status, detected_at, evidence)
SELECT v.tenant_id, v.vin, 'RANGE_LOW', (ARRAY['WARNING','CRITICAL','INFO'])[1 + (g % 3)],
       now() - (g || ' seconds')::interval - ((row_number() OVER ()) || ' milliseconds')::interval,
       (ARRAY['open','open','acknowledged','resolved'])[1 + (g % 4)], now() - (g * 3 || ' seconds')::interval, '{"bench":true}'
FROM (SELECT * FROM vehicle WHERE tenant_id = current_setting('app.bench_tenant')::uuid ORDER BY vin LIMIT 20000) v,
     generate_series(1, 75) g
ON CONFLICT DO NOTHING;
INSERT INTO trip (tenant_id, vin, started_at, ended_at, start_geohash, end_geohash, distance_km, energy_kwh)
SELECT v.tenant_id, v.vin, now() - (g * 40 || ' minutes')::interval, now() - (g * 40 - 20 || ' minutes')::interval, 'tdr1w2k', 'tdr1w3m', 5 + g % 20, 1 + g % 5
FROM (SELECT * FROM vehicle WHERE tenant_id = current_setting('app.bench_tenant')::uuid ORDER BY vin LIMIT 20000) v, generate_series(1, 75) g;
INSERT INTO audit_log (tenant_id, actor, actor_type, action, resource_type, resource_id, details, ts)
SELECT current_setting('app.bench_tenant')::uuid, 'bench-' || (g % 50), 'user', 'vehicle.read', 'vehicle', g::text, '{"status":200}', now() - (g || ' seconds')::interval
FROM generate_series(1, 600000) g;
ANALYZE alert; ANALYZE trip; ANALYZE audit_log;

SELECT 'rows: alert=' || (SELECT count(*) FROM alert) || ' trip=' || (SELECT count(*) FROM trip) || ' audit=' || (SELECT count(*) FROM audit_log) AS volume;

-- ---- (1) alert inbox: OFFSET pagination -> keyset + partial index -----------------------------------------------
\echo '=== (1) alert inbox BEFORE: ORDER BY ... LIMIT 50 OFFSET 30000 (page 600) ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT id, vin, rule, severity, status, detected_at FROM alert
WHERE tenant_id = current_setting('app.bench_tenant')::uuid AND status = 'open' ORDER BY detected_at DESC, id DESC LIMIT 50 OFFSET 30000;
\echo '=== (1) alert inbox BEFORE: keyset page, no suitable index ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT id, vin, rule, severity, status, detected_at FROM alert
WHERE tenant_id = current_setting('app.bench_tenant')::uuid AND status = 'open' AND (detected_at, id) < (now() - interval '80 hours', 'ffffffff-ffff-ffff-ffff-ffffffffffff')
ORDER BY detected_at DESC, id DESC LIMIT 50;
CREATE INDEX alert_inbox_open_idx ON alert (tenant_id, detected_at DESC, id DESC) WHERE status = 'open';
CREATE INDEX alert_inbox_all_idx ON alert (tenant_id, detected_at DESC, id DESC);
ANALYZE alert;
\echo '=== (1) alert inbox AFTER: keyset page + partial index (same query) ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT id, vin, rule, severity, status, detected_at FROM alert
WHERE tenant_id = current_setting('app.bench_tenant')::uuid AND status = 'open' AND (detected_at, id) < (now() - interval '80 hours', 'ffffffff-ffff-ffff-ffff-ffffffffffff')
ORDER BY detected_at DESC, id DESC LIMIT 50;

-- ---- (3) trips of one vehicle in a time window: composite index ----------------------------------------------------
\echo '=== (3) trips by vehicle and time BEFORE ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT id, started_at, distance_km, energy_kwh FROM trip
WHERE tenant_id = current_setting('app.bench_tenant')::uuid AND vin = (SELECT vin FROM trip WHERE tenant_id = current_setting('app.bench_tenant')::uuid LIMIT 1)
  AND started_at >= now() - interval '10 days' ORDER BY started_at DESC LIMIT 100;
CREATE INDEX trip_vin_time_idx ON trip (tenant_id, vin, started_at DESC);
CREATE INDEX trip_started_brin ON trip USING brin (started_at);
ANALYZE trip;
\echo '=== (3) trips by vehicle and time AFTER (composite index) ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT id, started_at, distance_km, energy_kwh FROM trip
WHERE tenant_id = current_setting('app.bench_tenant')::uuid AND vin = (SELECT vin FROM trip WHERE tenant_id = current_setting('app.bench_tenant')::uuid LIMIT 1)
  AND started_at >= now() - interval '10 days' ORDER BY started_at DESC LIMIT 100;

-- ---- (4) fleet energy report: aggregate over all trips -> materialised view, refreshed concurrently ---------------
\echo '=== (4) fleet daily energy report BEFORE: aggregate over 1.5M trips ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT date_trunc('day', started_at) AS day, count(*) AS trips, sum(distance_km) AS km, sum(energy_kwh) AS kwh FROM trip
WHERE tenant_id = current_setting('app.bench_tenant')::uuid GROUP BY 1 ORDER BY 1 DESC;
CREATE MATERIALIZED VIEW trip_daily AS
SELECT tenant_id, date_trunc('day', started_at) AS day, count(*) AS trips, sum(distance_km) AS km, sum(energy_kwh) AS kwh FROM trip GROUP BY 1, 2;
CREATE UNIQUE INDEX trip_daily_pk ON trip_daily (tenant_id, day);
\echo '=== (4) AFTER: read the materialised view ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT day, trips, km, kwh FROM trip_daily WHERE tenant_id = current_setting('app.bench_tenant')::uuid ORDER BY day DESC;
\echo '=== (4) REFRESH MATERIALIZED VIEW CONCURRENTLY works (readers are not blocked) ==='
REFRESH MATERIALIZED VIEW CONCURRENTLY trip_daily;

-- ---- (5) audit search by actor and time: composite index; the monthly partitions are pruned by the ts range -------
\echo '=== (5) audit by actor, last hour BEFORE ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT id, ts, action FROM audit_log WHERE tenant_id = current_setting('app.bench_tenant')::uuid AND actor = 'bench-7' AND ts >= now() - interval '1 hour' ORDER BY ts DESC LIMIT 100;
CREATE INDEX audit_actor_time_idx ON audit_log (tenant_id, actor, ts DESC);
ANALYZE audit_log;
\echo '=== (5) audit by actor, last hour AFTER ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT id, ts, action FROM audit_log WHERE tenant_id = current_setting('app.bench_tenant')::uuid AND actor = 'bench-7' AND ts >= now() - interval '1 hour' ORDER BY ts DESC LIMIT 100;

SELECT set_config('app.tenant_id', current_setting('app.bench_tenant'), false);
-- ---- (6) RLS predicate: per-row function call vs InitPlan (the policy form the schema uses) -------------------------
CREATE TABLE rls_scratch AS SELECT tenant_id, vin FROM vehicle;
CREATE INDEX ON rls_scratch (tenant_id);
ANALYZE rls_scratch;
\echo '=== (6) RLS predicate as a bare function call: evaluated per row candidate ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT count(*) FROM rls_scratch WHERE tenant_id = app_tenant();
\echo '=== (6) RLS predicate as (SELECT app_tenant()): InitPlan, index usable (what the policies use) ==='
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, SUMMARY ON)
SELECT count(*) FROM rls_scratch WHERE tenant_id = (SELECT app_tenant());

-- ---- cleanup ----
DROP TABLE rls_scratch;
DROP MATERIALIZED VIEW trip_daily;
DROP INDEX alert_inbox_open_idx, alert_inbox_all_idx, trip_vin_time_idx, trip_started_brin, audit_actor_time_idx;
DELETE FROM alert WHERE evidence @> '{"bench":true}';
DELETE FROM trip WHERE tenant_id = current_setting('app.bench_tenant')::uuid;
DELETE FROM audit_log WHERE actor LIKE 'bench-%';
