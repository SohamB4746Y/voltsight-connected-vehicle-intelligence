-- Extensions and least-privilege service roles.
-- Passwords are NOT set here; tools/dev/db_bootstrap sets them from environment secrets.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS btree_gist;

DO $$
DECLARE
  r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['voltsight_app', 'voltsight_gateway', 'voltsight_batch',
                           'voltsight_alerts', 'voltsight_privacy', 'voltsight_sealer']
  LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOINHERIT', r);
    END IF;
  END LOOP;
END
$$;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO voltsight_app, voltsight_gateway, voltsight_batch,
  voltsight_alerts, voltsight_privacy, voltsight_sealer;
