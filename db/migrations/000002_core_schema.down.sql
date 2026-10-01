DROP VIEW IF EXISTS vehicle_alert_summary;
DROP FUNCTION IF EXISTS tenant_has_feature(uuid, text);
DROP TABLE IF EXISTS incident_embedding, agent_action, agent_session, erasure_request, audit_log,
  alert_event, alert, charge_plan_item, charge_plan, soh_estimate, charge_session, trip, charger,
  vehicle_driver_assignment, driver, vehicle, depot, fleet, device_credential, user_role, app_user,
  subscription, tenant, model_version, role, plan_feature, plan, tariff_band, tariff_zone,
  vehicle_model, oem CASCADE;
DROP FUNCTION IF EXISTS app_tenant();
DROP FUNCTION IF EXISTS vin_valid(text);
