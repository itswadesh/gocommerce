#!/bin/sh
# Create the least-privileged custom-reports role when the Postgres data
# directory is first initialised (D78). The official postgres image runs every
# file in /docker-entrypoint-initdb.d once, on a fresh volume only, as the
# superuser — so this never runs against an existing database. For one of those,
# run scripts/reports-role.sql by hand once.
#
# The password comes from $REPORTS_DB_PASSWORD. Leave it unset and the role is
# not created: custom reports stay disabled, which is a safe default, not a
# broken one.
#
# No `set -e` and no top-level `exit`: a file here that is not executable (a bind
# mount from a Windows host is not) is *sourced* by the entrypoint, where either
# would abort the whole database initialisation. The work is guarded by an if
# instead, and psql's ON_ERROR_STOP reports a genuine SQL failure in the log.

if [ -n "$REPORTS_DB_PASSWORD" ]; then
	# grant_schema=no in platform mode, where this role is only a template and
	# the platform grants each store's own cloned role its schema (D79); the
	# shared schema must not be granted wholesale.
	psql -v ON_ERROR_STOP=1 \
		--username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
		-v reports_password="$REPORTS_DB_PASSWORD" \
		-v grant_schema="${REPORTS_GRANT_SCHEMA:-yes}" \
		-f /opt/gocommerce/reports-role.sql
	echo "initdb-reports-role: created the gocommerce_reports role."
else
	echo "initdb-reports-role: REPORTS_DB_PASSWORD not set; leaving custom reports disabled."
	echo "  Set it (and GOCOMMERCE_REPORTS_DATABASE_URL on the app) to turn them on."
fi
