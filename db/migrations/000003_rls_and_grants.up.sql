-- Row-level security and least-privilege grants (ADR-006).
--  * voltsight_app is the only role that serves user requests: every row access is bound to
--    app.tenant_id (set with SET LOCAL per request by the API).
--  * Service roles get explicit, narrow table privileges and explicit USING (true) policies;
--    nobody has BYPASSRLS. RLS is FORCED so even the table owner is subject to policy.

DO $$
DECLARE
  t text;
  owner_role text := current_user;
BEGIN
  -- Tables whose rows belong to exactly one tenant (tenant_id NOT NULL).
  FOREACH t IN ARRAY ARRAY['subscription', 'app_user', 'user_role', 'device_credential', 'fleet', 'depot',
      'vehicle', 'driver', 'vehicle_driver_assignment', 'trip', 'charge_session', 'soh_estimate',
      'charge_plan', 'charge_plan_item', 'alert', 'alert_event', 'audit_log', 'erasure_request',
      'agent_session', 'agent_action', 'incident_embedding']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I FOR ALL TO voltsight_app
                      USING (tenant_id = (SELECT app_tenant()))
                      WITH CHECK (tenant_id = (SELECT app_tenant()))$p$, t);
    EXECUTE format('CREATE POLICY owner_all ON %I FOR ALL TO %I USING (true) WITH CHECK (true)', t, owner_role);
  END LOOP;

  -- tenant: a request may see only its own tenant row.
  ALTER TABLE tenant ENABLE ROW LEVEL SECURITY;
  ALTER TABLE tenant FORCE ROW LEVEL SECURITY;
  EXECUTE 'CREATE POLICY tenant_isolation ON tenant FOR SELECT TO voltsight_app USING (id = (SELECT app_tenant()))';
  EXECUTE format('CREATE POLICY owner_all ON tenant FOR ALL TO %I USING (true) WITH CHECK (true)', owner_role);

  -- charger: public chargers (tenant_id IS NULL) are readable by all, writable by none of the tenants.
  ALTER TABLE charger ENABLE ROW LEVEL SECURITY;
  ALTER TABLE charger FORCE ROW LEVEL SECURITY;
  EXECUTE 'CREATE POLICY charger_read ON charger FOR SELECT TO voltsight_app
             USING (tenant_id IS NULL OR tenant_id = (SELECT app_tenant()))';
  EXECUTE 'CREATE POLICY charger_insert ON charger FOR INSERT TO voltsight_app
             WITH CHECK (tenant_id = (SELECT app_tenant()))';
  EXECUTE 'CREATE POLICY charger_update ON charger FOR UPDATE TO voltsight_app
             USING (tenant_id = (SELECT app_tenant())) WITH CHECK (tenant_id = (SELECT app_tenant()))';
  EXECUTE 'CREATE POLICY charger_delete ON charger FOR DELETE TO voltsight_app
             USING (tenant_id = (SELECT app_tenant()))';
  EXECUTE format('CREATE POLICY owner_all ON charger FOR ALL TO %I USING (true) WITH CHECK (true)', owner_role);

  -- Service roles: explicit permissive policies on the tables they are granted.
  FOREACH t IN ARRAY ARRAY['device_credential', 'vehicle'] LOOP
    EXECUTE format('CREATE POLICY gateway_all ON %I FOR SELECT TO voltsight_gateway USING (true)', t);
  END LOOP;
  EXECUTE 'CREATE POLICY gateway_audit ON audit_log FOR INSERT TO voltsight_gateway WITH CHECK (true)';

  FOREACH t IN ARRAY ARRAY['vehicle', 'alert', 'alert_event'] LOOP
    EXECUTE format('CREATE POLICY alerts_all ON %I FOR ALL TO voltsight_alerts USING (true) WITH CHECK (true)', t);
  END LOOP;
  EXECUTE 'CREATE POLICY alerts_audit ON audit_log FOR INSERT TO voltsight_alerts WITH CHECK (true)';

  FOREACH t IN ARRAY ARRAY['tenant', 'fleet', 'depot', 'vehicle', 'charger', 'trip', 'charge_session',
      'soh_estimate', 'alert', 'charge_plan', 'charge_plan_item', 'incident_embedding'] LOOP
    EXECUTE format('CREATE POLICY batch_all ON %I FOR ALL TO voltsight_batch USING (true) WITH CHECK (true)', t);
  END LOOP;
  EXECUTE 'CREATE POLICY batch_audit ON audit_log FOR INSERT TO voltsight_batch WITH CHECK (true)';

  FOREACH t IN ARRAY ARRAY['driver', 'vehicle_driver_assignment', 'trip', 'charge_session', 'erasure_request',
      'vehicle', 'incident_embedding'] LOOP
    EXECUTE format('CREATE POLICY privacy_all ON %I FOR ALL TO voltsight_privacy USING (true) WITH CHECK (true)', t);
  END LOOP;
  EXECUTE 'CREATE POLICY privacy_audit ON audit_log FOR INSERT TO voltsight_privacy WITH CHECK (true)';

  EXECUTE 'CREATE POLICY sealer_all ON audit_log FOR ALL TO voltsight_sealer USING (true) WITH CHECK (true)';
END
$$;

-- ---------------------------------------------------------------------------------------------
-- Grants (everything not listed is denied)
-- ---------------------------------------------------------------------------------------------
-- Application API role
GRANT SELECT ON tenant, subscription, trip, charge_session, soh_estimate, device_credential, audit_log,
  vehicle_model, oem, tariff_zone, tariff_band, plan, plan_feature, role, model_version TO voltsight_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON fleet, depot, vehicle, driver, vehicle_driver_assignment, app_user,
  user_role, charge_plan, charge_plan_item, agent_session, incident_embedding, charger TO voltsight_app;
GRANT SELECT, INSERT ON alert_event, erasure_request, audit_log TO voltsight_app;
GRANT SELECT ON alert TO voltsight_app;
GRANT UPDATE (status) ON alert TO voltsight_app;
GRANT SELECT, INSERT ON agent_action TO voltsight_app;
GRANT UPDATE (status, approver_id, result_hash) ON agent_action TO voltsight_app;
GRANT SELECT ON vehicle_alert_summary TO voltsight_app;

-- Ingest gateway: read-only credential and VIN ownership lookups
GRANT SELECT ON device_credential, vehicle, oem TO voltsight_gateway;
GRANT INSERT ON audit_log TO voltsight_gateway;

-- Alert service: the single writer of alert rows
GRANT SELECT ON vehicle TO voltsight_alerts;
GRANT SELECT, INSERT, UPDATE ON alert TO voltsight_alerts;
GRANT INSERT ON alert_event, audit_log TO voltsight_alerts;

-- Batch / ML jobs
GRANT SELECT ON tenant, fleet, depot, vehicle, charger, alert, vehicle_model, tariff_zone, tariff_band,
  oem TO voltsight_batch;
GRANT SELECT, INSERT, UPDATE, DELETE ON trip, charge_session, soh_estimate, charge_plan, charge_plan_item,
  incident_embedding TO voltsight_batch;
GRANT SELECT, INSERT ON model_version TO voltsight_batch;
GRANT INSERT ON audit_log TO voltsight_batch;

-- Privacy / erasure worker
GRANT SELECT, UPDATE, DELETE ON driver, vehicle_driver_assignment, trip, charge_session,
  incident_embedding TO voltsight_privacy;
GRANT SELECT, UPDATE ON erasure_request TO voltsight_privacy;
GRANT SELECT ON vehicle TO voltsight_privacy;
GRANT INSERT ON audit_log TO voltsight_privacy;

-- Audit sealer: may only assign chain fields
GRANT SELECT ON audit_log TO voltsight_sealer;
GRANT UPDATE (seq, prev_hash, hash) ON audit_log TO voltsight_sealer;

-- Monthly partitions for the audit log (default partition catches anything else).
CREATE FUNCTION audit_ensure_partitions(p_from date, p_months int) RETURNS int
LANGUAGE plpgsql AS $$
DECLARE
  m date;
  n int := 0;
  part text;
BEGIN
  FOR i IN 0..p_months - 1 LOOP
    m := (date_trunc('month', p_from) + make_interval(months => i))::date;
    part := format('audit_log_%s', to_char(m, 'YYYY_MM'));
    IF to_regclass(part) IS NULL THEN
      EXECUTE format('CREATE TABLE %I PARTITION OF audit_log FOR VALUES FROM (%L) TO (%L)',
                     part, m, (m + interval '1 month')::date);
      n := n + 1;
    END IF;
  END LOOP;
  RETURN n;
END
$$;

-- A partition cannot be created for a range whose rows already sit in the default partition, so
-- partitions are created ahead of the data: 36 months from 2026-01. A scheduled job calls
-- audit_ensure_partitions(current_date, 12) monthly to stay ahead.
SELECT audit_ensure_partitions('2026-01-01', 36);
