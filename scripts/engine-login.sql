-- The engine's own database login (D83).
--
-- Every store's rows sit in one set of tables, behind row-level-security
-- policies, and PostgreSQL lets a superuser (and a role with BYPASSRLS) past
-- every policy. So the engine must not sign in as one: it signs in as this
-- ordinary login, which owns the tables it creates and is held to their
-- policies. `gocommerce doctor` fails while it is not.
--
-- docker-compose.yml runs this on every start (the engine-login service), on
-- a fresh volume and an existing one alike. Without Compose, run it once as the
-- superuser, then point DATABASE_URL at gocommerce_app:
--
--   psql -U gocommerce -d gocommerce -v engine_password='...' -f engine-login.sql
--
-- It is safe to run twice.

\set ON_ERROR_STOP on

SELECT format('CREATE ROLE gocommerce_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', :'engine_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gocommerce_app')
\gexec

SELECT format('ALTER ROLE gocommerce_app PASSWORD %L', :'engine_password')
\gexec

GRANT CONNECT, TEMPORARY ON DATABASE :"DBNAME" TO gocommerce_app;
ALTER SCHEMA public OWNER TO gocommerce_app;

-- An existing install's tables were created by the superuser; hand them over,
-- or the new login could not migrate them. Nothing in a fresh database yet.
DO $$
DECLARE r record;
BEGIN
    FOR r IN SELECT c.relname, c.relkind FROM pg_class c
             JOIN pg_namespace n ON n.oid = c.relnamespace
             WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
    LOOP
        EXECUTE format('ALTER %s public.%I OWNER TO gocommerce_app',
            CASE r.relkind WHEN 'S' THEN 'SEQUENCE' WHEN 'v' THEN 'VIEW'
                           WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'f' THEN 'FOREIGN TABLE'
                           ELSE 'TABLE' END, r.relname);
    END LOOP;
    FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p
             JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'
    LOOP
        EXECUTE format('ALTER ROUTINE %s OWNER TO gocommerce_app', r.sig);
    END LOOP;
END $$;
