-- The role custom reports run as (D78).
--
-- Custom reports let an operator save a SELECT and run it against the store's
-- database. That SQL reaches the database directly, so the one thing standing
-- between a report and the whole server is the role it runs as. A read-only
-- transaction is not enough: it stops writes and schema changes, but
-- PostgreSQL's maintenance functions — terminating a backend, creating a
-- replication slot, reading a file off the server — are plain SELECTs it
-- allows, and under a superuser role (the default role in both compose files)
-- they do real damage. So reports run as a separate login that was granted
-- nothing but the right to read.
--
-- Run this ONCE per database, as a superuser or the database owner, then point
-- the engine at the role:
--
--   psql "$DATABASE_URL" \
--     -v reports_password="a-strong-password" \
--     -f scripts/reports-role.sql
--
--   GOCOMMERCE_REPORTS_DATABASE_URL=
--     postgres://gocommerce_reports:a-strong-password@HOST:PORT/DB?sslmode=disable
--
-- It is idempotent: run it again to re-grant after adding tables, or to change
-- the password. The role name and the schema are psql variables with defaults;
-- override them with -v role_name=... and -v target_schema=... if you need to.
--
-- Platform mode (the `platform` command) needs only this role to exist with a
-- password and CONNECT on the database: it is a template. Pass -v grant_schema=no
-- there — the platform gives each store its own role cloned from this one,
-- granted read on only that store's schema (D79), and reports never run as this
-- one. Granting it any schema would hand that reach to every store at once.

\set ON_ERROR_STOP on

-- Defaults. `reports_password` has none: a report role with a blank or guessed
-- password is no barrier, so the script refuses to run without one.
\if :{?role_name}
\else
  \set role_name gocommerce_reports
\endif
\if :{?target_schema}
\else
  \set target_schema public
\endif
\if :{?grant_schema}
\else
  \set grant_schema yes
\endif
\if :{?reports_password}
\else
  \echo 'ERROR: pass -v reports_password=... (the password the engine will use to log in)'
  \quit 1
\endif

-- The role: can log in, can do nothing else. Created if absent, then its
-- attributes and password are set unconditionally, so re-running the script
-- rotates the password. format() with %I and %L quotes the name and password
-- safely; a plain DO block cannot be used because psql does not substitute its
-- variables inside a dollar-quoted body.
SELECT format(
  'CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
  :'role_name', :'reports_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role_name')
\gexec

SELECT format(
  'ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
  :'role_name', :'reports_password')
\gexec

-- Connect is always needed. The schema grants are skipped in platform mode,
-- where this role is a template and the platform grants each store's own cloned
-- role its schema instead (D79).
GRANT CONNECT ON DATABASE :"DBNAME" TO :"role_name";

\if :grant_schema
  -- Read, and only read, the target schema.
  GRANT USAGE ON SCHEMA :"target_schema" TO :"role_name";
  GRANT SELECT ON ALL TABLES IN SCHEMA :"target_schema" TO :"role_name";
  GRANT SELECT ON ALL SEQUENCES IN SCHEMA :"target_schema" TO :"role_name";

  -- Tables a later migration creates are readable too, without re-running this —
  -- as long as they are created by the same role that owns the existing ones
  -- (the engine's own role, which is what ran the migrations above).
  ALTER DEFAULT PRIVILEGES IN SCHEMA :"target_schema" GRANT SELECT ON TABLES TO :"role_name";
  ALTER DEFAULT PRIVILEGES IN SCHEMA :"target_schema" GRANT SELECT ON SEQUENCES TO :"role_name";
  \echo 'Done. Granted read on schema' :'target_schema' 'to' :'role_name'
\else
  \echo 'Done. Role' :'role_name' 'created with CONNECT only (platform mode clones a per-store role from it)'
\endif
