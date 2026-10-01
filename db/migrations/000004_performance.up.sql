-- Indexes and the trip rollup proven in G9b (evidence/G9b/plans/sqlopt.txt: real EXPLAIN ANALYZE before/after).

-- alert inbox: keyset pagination (detected_at, id) per tenant; the partial index serves the default "open" view
CREATE INDEX alert_inbox_open_idx ON alert (tenant_id, detected_at DESC, id DESC) WHERE status = 'open';
CREATE INDEX alert_inbox_all_idx ON alert (tenant_id, detected_at DESC, id DESC);

-- trips of one vehicle in a time window
CREATE INDEX trip_vin_time_idx ON trip (tenant_id, vin, started_at DESC);

-- audit search by actor and time (the monthly partitions are pruned by the ts range)
CREATE INDEX audit_actor_time_idx ON audit_log (tenant_id, actor, ts DESC);

-- daily energy rollup. Materialised views cannot carry row-level security, so the application role never sees the
-- view itself: it reads trip_daily_v, a security-barrier view that filters on the request's tenant.
CREATE MATERIALIZED VIEW trip_daily AS
SELECT tenant_id, date_trunc('day', started_at) AS day, count(*)::int AS trips, sum(distance_km)::float8 AS km, sum(energy_kwh)::float8 AS kwh
FROM trip GROUP BY 1, 2;
CREATE UNIQUE INDEX trip_daily_pk ON trip_daily (tenant_id, day);   -- required for REFRESH ... CONCURRENTLY

CREATE VIEW trip_daily_v WITH (security_barrier = true) AS
SELECT day, trips, km, kwh FROM trip_daily WHERE tenant_id = (SELECT app_tenant());

REVOKE ALL ON trip_daily FROM PUBLIC;
GRANT SELECT ON trip_daily_v TO voltsight_app;
GRANT SELECT ON trip_daily TO voltsight_batch;

-- the batch role is not the owner of the view, so it refreshes it through a narrow SECURITY DEFINER function
CREATE FUNCTION refresh_trip_daily() RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
BEGIN
  REFRESH MATERIALIZED VIEW trip_daily;
END
$$;
REVOKE ALL ON FUNCTION refresh_trip_daily() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION refresh_trip_daily() TO voltsight_batch;
