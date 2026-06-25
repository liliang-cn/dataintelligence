-- Warehouse-level row security for on-behalf-of sessions. `di obo setup` applies
-- this (the docker init scripts only run on first volume init).
--
-- Identity is enforced by the database itself: a session does SET LOCAL ROLE to
-- the least-privilege di_app role (superusers bypass RLS, so the app must NOT be
-- one), then sets app.region via set_config. This policy filters stores to that
-- region. With app.region empty/unset, all rows are visible — so unscoped roles
-- (admin/finance/analyst) are unaffected, while a manager session is confined to
-- its region even if the app layer missed it.

-- Least-privilege role the app drops into (NOLOGIN: only reachable via SET ROLE).
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'di_app') THEN
    CREATE ROLE di_app NOLOGIN;
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO di_app;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO di_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO di_app;

ALTER TABLE stores ENABLE ROW LEVEL SECURITY;
ALTER TABLE stores FORCE ROW LEVEL SECURITY; -- apply even to the table owner

DROP POLICY IF EXISTS stores_region_isolation ON stores;
CREATE POLICY stores_region_isolation ON stores
  FOR SELECT TO di_app
  USING (
    coalesce(current_setting('app.region', true), '') = ''
    OR region = current_setting('app.region', true)
  );
