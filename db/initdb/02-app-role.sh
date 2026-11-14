#!/usr/bin/env bash
# Creates the least-privilege role the application connects as. Runs once, after 01-schema.sql.
# The permissions themselves live in db/grants.psql so they can be re-applied after migrations.
#
# The image entrypoint either executes this file or sources it into its own shell, so it
# deliberately sets no shell options and never calls exit.
: "${APP_DB_USER:?APP_DB_USER is required}"
: "${APP_DB_PASSWORD:?APP_DB_PASSWORD is required}"
GRANTS_FILE="${GRANTS_FILE:-/opt/omnipos/grants.psql}"

# The two commands are chained, so the script's exit status is that of the first one to fail and a
# failed role creation is never reported as success.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
     -v app_user="$APP_DB_USER" -v app_password="$APP_DB_PASSWORD" -v db_name="$POSTGRES_DB" <<'SQL' &&
CREATE ROLE :"app_user" LOGIN PASSWORD :'app_password'
    NOSUPERUSER NOCREATEDB NOCREATEROLE;

REVOKE ALL ON DATABASE :"db_name" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db_name" TO :"app_user";
SQL

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
     -v app_user="$APP_DB_USER" -f "$GRANTS_FILE"
