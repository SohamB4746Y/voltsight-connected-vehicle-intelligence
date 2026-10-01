-- VoltSight relational core (3NF). See docs/3nf.md for the functional-dependency analysis and
-- the documented, FK-enforced denormalisation of tenant_id (ADR-006).

-- ---------------------------------------------------------------------------------------------
-- Helpers
-- ---------------------------------------------------------------------------------------------
-- ISO 3779 VIN check: 17 chars from [A-HJ-NPR-Z0-9] (no I, O, Q) and a valid check digit at position 9.
CREATE FUNCTION vin_valid(v text) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE
  letters  constant text  := 'ABCDEFGHJKLMNPRSTUVWXYZ';
  lvalues  constant int[] := ARRAY[1,2,3,4,5,6,7,8,1,2,3,4,5,7,9,2,3,4,5,6,7,8,9];
  weights  constant int[] := ARRAY[8,7,6,5,4,3,2,10,0,9,8,7,6,5,4,3,2];
  total    int := 0;
  c        text;
  val      int;
  i        int;
  expected text;
BEGIN
  IF v !~ '^[A-HJ-NPR-Z0-9]{17}$' THEN
    RETURN false;
  END IF;
  FOR i IN 1..17 LOOP
    c := substr(v, i, 1);
    IF c ~ '[0-9]' THEN
      val := c::int;
    ELSE
      val := lvalues[strpos(letters, c)];
    END IF;
    total := total + val * weights[i];
  END LOOP;
  expected := CASE WHEN total % 11 = 10 THEN 'X' ELSE (total % 11)::text END;
  RETURN substr(v, 9, 1) = expected;
END
$$;

-- Tenant of the current request. Fail-closed: unset -> NULL -> predicates match nothing;
-- a malformed value raises an error (never silently widens access).
CREATE FUNCTION app_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT nullif(current_setting('app.tenant_id', true), '')::uuid
$$;

-- ---------------------------------------------------------------------------------------------
-- Reference data (global, read-only to the application)
-- ---------------------------------------------------------------------------------------------
CREATE TABLE oem (
  code    text PRIMARY KEY,
  dialect text NOT NULL CHECK (dialect IN ('A', 'B'))
);

CREATE TABLE vehicle_model (
  id                  smallint PRIMARY KEY,
  oem_code            text NOT NULL REFERENCES oem (code),
  name                text NOT NULL,
  battery_kwh_nominal numeric(6, 2) NOT NULL CHECK (battery_kwh_nominal > 0),
  kwh_per_100km_wltp  numeric(5, 2) NOT NULL CHECK (kwh_per_100km_wltp > 0),
  max_dc_kw           numeric(6, 1) NOT NULL CHECK (max_dc_kw > 0),
  UNIQUE (oem_code, name)
);

CREATE TABLE tariff_zone (
  id       smallint PRIMARY KEY,
  name     text NOT NULL UNIQUE,
  currency char(3) NOT NULL DEFAULT 'INR'
);

CREATE TABLE tariff_band (
  zone_id       smallint NOT NULL REFERENCES tariff_zone (id),
  hour_from     smallint NOT NULL CHECK (hour_from BETWEEN 0 AND 23),
  hour_to       smallint NOT NULL CHECK (hour_to BETWEEN 1 AND 24),
  price_per_kwh numeric(8, 4) NOT NULL CHECK (price_per_kwh >= 0),
  PRIMARY KEY (zone_id, hour_from),
  CHECK (hour_to > hour_from)
);

CREATE TABLE plan (
  code text PRIMARY KEY
);

CREATE TABLE plan_feature (
  plan_code text NOT NULL REFERENCES plan (code),
  feature   text NOT NULL,
  PRIMARY KEY (plan_code, feature)
);

CREATE TABLE role (
  code text PRIMARY KEY
);

CREATE TABLE model_version (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name         text NOT NULL,
  version      text NOT NULL,
  artefact_uri text NOT NULL,
  sha256       char(64) NOT NULL,
  metrics      jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (name, version)
);

-- ---------------------------------------------------------------------------------------------
-- Tenancy, identity, entitlements
-- ---------------------------------------------------------------------------------------------
CREATE TABLE tenant (
  id         uuid PRIMARY KEY,
  name       text NOT NULL UNIQUE,
  region     text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE subscription (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  plan_code  text NOT NULL REFERENCES plan (code),
  valid_from timestamptz NOT NULL DEFAULT now(),
  valid_to   timestamptz,
  CHECK (valid_to IS NULL OR valid_to > valid_from)
);
CREATE INDEX subscription_tenant_idx ON subscription (tenant_id);
CREATE INDEX subscription_plan_idx ON subscription (plan_code);

CREATE TABLE app_user (
  id           uuid PRIMARY KEY,                    -- equals the Keycloak subject
  tenant_id    uuid NOT NULL REFERENCES tenant (id),
  email        text NOT NULL,
  display_name text NOT NULL,
  active       boolean NOT NULL DEFAULT true,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, email)
);

CREATE TABLE user_role (
  tenant_id uuid NOT NULL,
  user_id   uuid NOT NULL,
  role_code text NOT NULL REFERENCES role (code),
  PRIMARY KEY (user_id, role_code),
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user (tenant_id, id)
);
CREATE INDEX user_role_tenant_user_idx ON user_role (tenant_id, user_id);
CREATE INDEX user_role_role_idx ON user_role (role_code);

CREATE TABLE device_credential (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenant (id),
  oem_code         text NOT NULL REFERENCES oem (code),
  cert_serial      text NOT NULL UNIQUE,
  cert_fingerprint char(64) NOT NULL UNIQUE,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  not_before       timestamptz NOT NULL,
  not_after        timestamptz NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  revoked_at       timestamptz,
  CHECK (not_after > not_before),
  CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);
CREATE INDEX device_credential_tenant_idx ON device_credential (tenant_id);
CREATE INDEX device_credential_oem_idx ON device_credential (oem_code);

-- ---------------------------------------------------------------------------------------------
-- Fleet domain
-- ---------------------------------------------------------------------------------------------
CREATE TABLE fleet (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name)
);

CREATE TABLE depot (
  id        uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  fleet_id  uuid NOT NULL,
  name      text NOT NULL,
  city      text NOT NULL,
  lat       double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
  lon       double precision NOT NULL CHECK (lon BETWEEN -180 AND 180),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, fleet_id, name),
  FOREIGN KEY (tenant_id, fleet_id) REFERENCES fleet (tenant_id, id)
);
CREATE INDEX depot_tenant_fleet_idx ON depot (tenant_id, fleet_id);

CREATE TABLE vehicle (
  vin           char(17) PRIMARY KEY CHECK (vin_valid(vin::text)),
  tenant_id     uuid NOT NULL,
  fleet_id      uuid NOT NULL,
  model_id      smallint NOT NULL REFERENCES vehicle_model (id),
  home_depot_id uuid,
  commissioned_at date NOT NULL,
  UNIQUE (tenant_id, vin),
  FOREIGN KEY (tenant_id, fleet_id) REFERENCES fleet (tenant_id, id),
  FOREIGN KEY (tenant_id, home_depot_id) REFERENCES depot (tenant_id, id)
);
CREATE INDEX vehicle_tenant_fleet_idx ON vehicle (tenant_id, fleet_id);
CREATE INDEX vehicle_model_idx ON vehicle (model_id);
CREATE INDEX vehicle_tenant_depot_idx ON vehicle (tenant_id, home_depot_id);

CREATE TABLE driver (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  pseudonym  text NOT NULL,
  pii_enc    bytea,                       -- pgcrypto-encrypted name/phone; NULL after erasure
  created_at timestamptz NOT NULL DEFAULT now(),
  erased_at  timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, pseudonym),
  CHECK ((erased_at IS NULL) = (pii_enc IS NOT NULL))
);

CREATE TABLE vehicle_driver_assignment (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  vin        char(17) NOT NULL,
  driver_id  uuid NOT NULL,
  valid_from timestamptz NOT NULL,
  valid_to   timestamptz,
  FOREIGN KEY (tenant_id, vin) REFERENCES vehicle (tenant_id, vin),
  FOREIGN KEY (tenant_id, driver_id) REFERENCES driver (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to > valid_from),
  -- a vehicle has at most one driver at any instant
  EXCLUDE USING gist (vin WITH =, tstzrange(valid_from, valid_to) WITH &&)
);
CREATE INDEX vda_tenant_vin_idx ON vehicle_driver_assignment (tenant_id, vin);
CREATE INDEX vda_tenant_driver_idx ON vehicle_driver_assignment (tenant_id, driver_id);

CREATE TABLE charger (
  id           uuid PRIMARY KEY,
  tenant_id    uuid,                      -- NULL = public network, visible to all tenants
  depot_id     uuid,
  name         text NOT NULL,
  lat          double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
  lon          double precision NOT NULL CHECK (lon BETWEEN -180 AND 180),
  geohash      text NOT NULL,
  power_kw     numeric(6, 1) NOT NULL CHECK (power_kw > 0),
  tariff_zone  smallint NOT NULL REFERENCES tariff_zone (id),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, depot_id) REFERENCES depot (tenant_id, id),
  CHECK (depot_id IS NULL OR tenant_id IS NOT NULL)
);
CREATE INDEX charger_tenant_depot_idx ON charger (tenant_id, depot_id);
CREATE INDEX charger_tariff_zone_idx ON charger (tariff_zone);
CREATE INDEX charger_geohash_idx ON charger (geohash text_pattern_ops);

CREATE TABLE trip (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  vin          char(17) NOT NULL,
  started_at   timestamptz NOT NULL,
  ended_at     timestamptz NOT NULL,
  start_geohash text NOT NULL,
  end_geohash   text NOT NULL,
  distance_km  numeric(8, 2) NOT NULL CHECK (distance_km >= 0),
  energy_kwh   numeric(8, 3) NOT NULL CHECK (energy_kwh >= -50),   -- regen can make short trips slightly negative
  FOREIGN KEY (tenant_id, vin) REFERENCES vehicle (tenant_id, vin),
  CHECK (ended_at >= started_at)
);
CREATE INDEX trip_tenant_vin_idx ON trip (tenant_id, vin);

CREATE TABLE charge_session (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  vin        char(17) NOT NULL,
  charger_id uuid NOT NULL REFERENCES charger (id),
  started_at timestamptz NOT NULL,
  ended_at   timestamptz NOT NULL,
  soc_start  numeric(5, 2) NOT NULL CHECK (soc_start BETWEEN 0 AND 100),
  soc_end    numeric(5, 2) NOT NULL CHECK (soc_end BETWEEN 0 AND 100),
  kwh        numeric(8, 3) NOT NULL CHECK (kwh >= 0),
  FOREIGN KEY (tenant_id, vin) REFERENCES vehicle (tenant_id, vin),
  CHECK (ended_at > started_at)
);
CREATE INDEX charge_session_tenant_vin_idx ON charge_session (tenant_id, vin);
CREATE INDEX charge_session_charger_idx ON charge_session (charger_id);

CREATE TABLE soh_estimate (
  tenant_id        uuid NOT NULL,
  vin              char(17) NOT NULL,
  as_of            timestamptz NOT NULL,
  method           text NOT NULL,
  capacity_kwh     numeric(7, 3) NOT NULL CHECK (capacity_kwh > 0),
  model_version_id uuid REFERENCES model_version (id),
  PRIMARY KEY (vin, as_of, method),
  FOREIGN KEY (tenant_id, vin) REFERENCES vehicle (tenant_id, vin)
);
CREATE INDEX soh_estimate_tenant_vin_idx ON soh_estimate (tenant_id, vin);
CREATE INDEX soh_estimate_model_idx ON soh_estimate (model_version_id);

-- ---------------------------------------------------------------------------------------------
-- Charge plans (human-approved actions)
-- ---------------------------------------------------------------------------------------------
CREATE TABLE charge_plan (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id              uuid NOT NULL,
  status                 text NOT NULL DEFAULT 'proposed'
                           CHECK (status IN ('proposed', 'approved', 'rejected', 'cancelled')),
  created_by             uuid NOT NULL,
  approved_by            uuid,
  created_at             timestamptz NOT NULL DEFAULT now(),
  approved_at            timestamptz,
  cost_estimate          numeric(12, 2),
  baseline_cost_estimate numeric(12, 2),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, created_by) REFERENCES app_user (tenant_id, id),
  FOREIGN KEY (tenant_id, approved_by) REFERENCES app_user (tenant_id, id),
  CHECK ((status = 'approved') = (approved_by IS NOT NULL AND approved_at IS NOT NULL))
);
CREATE INDEX charge_plan_tenant_status_idx ON charge_plan (tenant_id, status);
CREATE INDEX charge_plan_created_by_idx ON charge_plan (tenant_id, created_by);
CREATE INDEX charge_plan_approved_by_idx ON charge_plan (tenant_id, approved_by);

CREATE TABLE charge_plan_item (
  tenant_id  uuid NOT NULL,
  plan_id    uuid NOT NULL,
  vin        char(17) NOT NULL,
  charger_id uuid NOT NULL REFERENCES charger (id),
  start_at   timestamptz NOT NULL,
  end_at     timestamptz NOT NULL,
  target_soc numeric(5, 2) NOT NULL CHECK (target_soc BETWEEN 0 AND 100),
  PRIMARY KEY (plan_id, vin, start_at),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES charge_plan (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, vin) REFERENCES vehicle (tenant_id, vin),
  CHECK (end_at > start_at)
);
CREATE INDEX charge_plan_item_tenant_plan_idx ON charge_plan_item (tenant_id, plan_id);
CREATE INDEX charge_plan_item_tenant_vin_idx ON charge_plan_item (tenant_id, vin);
CREATE INDEX charge_plan_item_charger_idx ON charge_plan_item (charger_id);

-- ---------------------------------------------------------------------------------------------
-- Alerts
-- ---------------------------------------------------------------------------------------------
CREATE TABLE alert (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL,
  vin             char(17) NOT NULL,
  rule            text NOT NULL CHECK (rule IN ('RANGE_CRITICAL', 'RANGE_LOW', 'CHARGER_UNAVAILABLE_ON_ROUTE',
                                                'UNAPPROVED_DEPOT_CLUSTER', 'SOH_DROP', 'THERMAL_FAULT_DTC')),
  severity        text NOT NULL CHECK (severity IN ('INFO', 'WARNING', 'CRITICAL')),
  window_start    timestamptz NOT NULL,
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'acknowledged', 'resolved')),
  detected_at     timestamptz NOT NULL,
  source_event_ts timestamptz,
  source_recv_ts  timestamptz,
  evidence        jsonb NOT NULL DEFAULT '{}'::jsonb,   -- immutable point-in-time snapshot (intentional denormalisation)
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (vin, rule, window_start),                     -- idempotency key
  FOREIGN KEY (tenant_id, vin) REFERENCES vehicle (tenant_id, vin)
);
CREATE INDEX alert_tenant_vin_idx ON alert (tenant_id, vin);

CREATE TABLE alert_event (
  id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id uuid NOT NULL,
  alert_id  uuid NOT NULL,
  actor     uuid NOT NULL,
  action    text NOT NULL CHECK (action IN ('acknowledge', 'resolve', 'reopen', 'comment')),
  at        timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, alert_id) REFERENCES alert (tenant_id, id),
  FOREIGN KEY (tenant_id, actor) REFERENCES app_user (tenant_id, id)
);
CREATE INDEX alert_event_tenant_alert_idx ON alert_event (tenant_id, alert_id);
CREATE INDEX alert_event_tenant_actor_idx ON alert_event (tenant_id, actor);

-- ---------------------------------------------------------------------------------------------
-- Compliance
-- ---------------------------------------------------------------------------------------------
-- Append-only, tamper-evident. No FK to tenant: audit records must outlive tenant deletion.
-- seq/prev_hash/hash are assigned by the sealer role (per-tenant hash chain); uniqueness of
-- (tenant_id, seq) cannot be a DB constraint on a partitioned table, the chain verifier checks it.
CREATE TABLE audit_log (
  id            bigint GENERATED ALWAYS AS IDENTITY,
  ts            timestamptz NOT NULL DEFAULT now(),
  tenant_id     uuid NOT NULL,
  actor         text NOT NULL,
  actor_type    text NOT NULL CHECK (actor_type IN ('user', 'service', 'agent', 'device')),
  action        text NOT NULL,
  resource_type text NOT NULL,
  resource_id   text,
  subject_id    uuid,                     -- data subject (e.g. driver) whose data was touched
  request_id    text,
  details       jsonb NOT NULL DEFAULT '{}'::jsonb,
  seq           bigint,
  prev_hash     bytea,
  hash          bytea,
  PRIMARY KEY (id, ts)
) PARTITION BY RANGE (ts);
CREATE TABLE audit_log_default PARTITION OF audit_log DEFAULT;
CREATE INDEX audit_log_tenant_ts_idx ON audit_log (tenant_id, ts DESC);
CREATE INDEX audit_log_subject_idx ON audit_log (tenant_id, subject_id, ts DESC) WHERE subject_id IS NOT NULL;

CREATE TABLE erasure_request (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL,
  subject_id   uuid NOT NULL,
  requested_by uuid NOT NULL,
  status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
  requested_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  verification jsonb,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, requested_by) REFERENCES app_user (tenant_id, id)
);
CREATE INDEX erasure_request_tenant_requested_by_idx ON erasure_request (tenant_id, requested_by);

-- ---------------------------------------------------------------------------------------------
-- Agent
-- ---------------------------------------------------------------------------------------------
CREATE TABLE agent_session (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  user_id    uuid NOT NULL,
  llm        text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user (tenant_id, id)
);
CREATE INDEX agent_session_tenant_user_idx ON agent_session (tenant_id, user_id);

CREATE TABLE agent_action (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL,
  session_id  uuid NOT NULL,
  tool        text NOT NULL,
  args_hash   char(64) NOT NULL,
  status      text NOT NULL CHECK (status IN ('proposed', 'approved', 'rejected', 'executed', 'failed')),
  approver_id uuid,
  result_hash char(64),
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, session_id) REFERENCES agent_session (tenant_id, id),
  FOREIGN KEY (tenant_id, approver_id) REFERENCES app_user (tenant_id, id)
);
CREATE INDEX agent_action_tenant_session_idx ON agent_action (tenant_id, session_id);
CREATE INDEX agent_action_tenant_approver_idx ON agent_action (tenant_id, approver_id);

CREATE TABLE incident_embedding (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  kind       text NOT NULL,
  source_ref text,
  body       text NOT NULL,
  embedding  vector(384) NOT NULL,
  meta       jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incident_embedding_tenant_kind_idx ON incident_embedding (tenant_id, kind);
CREATE INDEX incident_embedding_hnsw_idx ON incident_embedding USING hnsw (embedding vector_cosine_ops);

-- ---------------------------------------------------------------------------------------------
-- Entitlements and API-facing views (security_invoker: RLS of the caller applies)
-- ---------------------------------------------------------------------------------------------
CREATE FUNCTION tenant_has_feature(p_tenant uuid, p_feature text) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT EXISTS (
    SELECT 1
    FROM subscription s
    JOIN plan_feature pf ON pf.plan_code = s.plan_code
    WHERE s.tenant_id = p_tenant
      AND pf.feature = p_feature
      AND s.valid_from <= now()
      AND (s.valid_to IS NULL OR s.valid_to > now())
  )
$$;

CREATE VIEW vehicle_alert_summary WITH (security_invoker = true) AS
SELECT v.tenant_id,
       v.vin,
       v.fleet_id,
       count(a.id) FILTER (WHERE a.status = 'open') AS open_alerts
FROM vehicle v
LEFT JOIN alert a ON a.tenant_id = v.tenant_id AND a.vin = v.vin
GROUP BY v.tenant_id, v.vin, v.fleet_id;
