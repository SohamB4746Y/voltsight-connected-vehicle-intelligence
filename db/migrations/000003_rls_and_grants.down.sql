DROP FUNCTION IF EXISTS audit_ensure_partitions(date, int);

DO $$
DECLARE
  rec record;
BEGIN
  FOR rec IN SELECT schemaname, tablename, policyname FROM pg_policies WHERE schemaname = 'public' LOOP
    EXECUTE format('DROP POLICY %I ON %I.%I', rec.policyname, rec.schemaname, rec.tablename);
  END LOOP;
  FOR rec IN SELECT c.relname FROM pg_class c
             WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p') AND c.relrowsecurity LOOP
    EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', rec.relname);
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', rec.relname);
  END LOOP;
END
$$;

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM voltsight_app, voltsight_gateway, voltsight_batch,
  voltsight_alerts, voltsight_privacy, voltsight_sealer;
