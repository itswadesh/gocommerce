#!/bin/sh
# Create the engine's own login when the Postgres data directory is first
# initialised (D83): an ordinary login that owns the tables and is held to
# their row policies, because a superuser would pass every policy and stores
# would not be kept apart. Runs once, on a fresh volume only; for an existing
# database run scripts/engine-login.sql by hand once.
#
# Its password is $ENGINE_DB_PASSWORD, or $POSTGRES_PASSWORD when that is not
# set. Like initdb-reports-role.sh, no `set -e` and no top-level `exit`: a
# file mounted from a Windows host is sourced, not executed.

psql -v ON_ERROR_STOP=1 \
	--username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
	-v engine_password="${ENGINE_DB_PASSWORD:-$POSTGRES_PASSWORD}" \
	-f /opt/gocommerce/engine-login.sql
echo "initdb-engine-login: created the gocommerce_app login."
