#!/bin/sh
# Creates the two database roles used by ledgerd. Runs once, when the
# PostgreSQL data directory is first initialised (docker-entrypoint-initdb.d).
#
#   ledgerd_owner  owns the schema and runs migrations. Used only by `ledgerd migrate`.
#   ledgerd_app    used by the running service. Least privilege: it cannot alter
#                  or delete history, and cannot change the schema.
#
# Required environment (set by docker-compose or by the test harness):
#   LEDGERD_OWNER_PASSWORD, LEDGERD_APP_PASSWORD
set -eu

: "${LEDGERD_OWNER_PASSWORD:?LEDGERD_OWNER_PASSWORD is required}"
: "${LEDGERD_APP_PASSWORD:?LEDGERD_APP_PASSWORD is required}"

# The heredoc is quoted, so the shell does not expand anything inside it; psql
# substitutes :'var' (as a quoted literal) and :"var" (as a quoted identifier).
psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v owner_pw="$LEDGERD_OWNER_PASSWORD" \
  -v app_pw="$LEDGERD_APP_PASSWORD" \
  -v dbname="$POSTGRES_DB" <<'SQL'
CREATE ROLE ledgerd_owner LOGIN PASSWORD :'owner_pw';
CREATE ROLE ledgerd_app   LOGIN PASSWORD :'app_pw';

-- The owner owns the database (and therefore the public schema in PostgreSQL 15+).
ALTER DATABASE :"dbname" OWNER TO ledgerd_owner;

-- Only the two ledgerd roles (and superusers) may connect.
REVOKE ALL ON DATABASE :"dbname" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"dbname" TO ledgerd_owner, ledgerd_app;
SQL
