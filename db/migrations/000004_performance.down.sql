DROP FUNCTION IF EXISTS refresh_trip_daily();
DROP VIEW IF EXISTS trip_daily_v;
DROP MATERIALIZED VIEW IF EXISTS trip_daily;
DROP INDEX IF EXISTS audit_actor_time_idx;
DROP INDEX IF EXISTS trip_vin_time_idx;
DROP INDEX IF EXISTS alert_inbox_all_idx;
DROP INDEX IF EXISTS alert_inbox_open_idx;
