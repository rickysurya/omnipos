# 0002: Database image and access

Status: accepted, 2026-11-12

## Context

`docker-compose.yml` used `postgres:15-alpine`, published the port on every interface, passed the owner credentials to the application, and had no healthcheck. PostgreSQL supports each major version for five years after its first release:

| Major | Final release |
|---|---|
| 14 | 2026-11-12 |
| 15 | 2027-11-11 |
| 16 | 2028-11-09 |
| 17 | 2029-11-08 |
| 18 | 2030-11-14 |

Source: postgresql.org/support/versioning.

## Decisions

PostgreSQL 18 (`postgres:18-trixie`).
- Version 15 has one year of support left. Version 18 has the longest runway, and this is a new database with no data to carry across.
- The tag follows minor releases, which the PostgreSQL project recommends running. For production, pin the image by digest and update it on purpose.
- The Debian variant is used instead of Alpine. Alpine uses musl, whose locale and collation behavior differs from glibc, and index ordering depends on collation.

Data volume.
- The 18 image keeps data in a versioned directory under `/var/lib/postgresql` (`PGDATA` is `/var/lib/postgresql/18/docker`), so the volume mounts at `/var/lib/postgresql`. The old mount path fails at container start.
- A new named volume, `pgdata18`, leaves the old PostgreSQL 15 volume untouched. A major version cannot reuse another major's data directory. To keep existing data, `pg_dump` from the old container and restore into the new one.

Network and secrets.
- The port binds to `127.0.0.1`. Publishing it on all interfaces would expose the database to the local network.
- `POSTGRES_USER`, `POSTGRES_PASSWORD` and `APP_DB_PASSWORD` are required. Compose refuses to start without them, so there is no default `admin/admin`.
- A healthcheck lets dependent tooling wait for a ready database.

Two roles.
- The owner role (`POSTGRES_USER`) creates the schema and runs migrations.
- The application connects as `omnipos_app`, which holds explicit per table grants and nothing else: `SELECT`, `INSERT` and `UPDATE` on `customers` and `orders`, `SELECT` and `INSERT` on `order_items` and `processed_updates`, and `SELECT` on `menu_items`. It cannot `DELETE`, `TRUNCATE`, `DROP`, `ALTER` or create objects.
- This makes the soft deletion rule in the architecture a database guarantee. If the application is compromised, order history cannot be erased and the schema cannot be changed.
- There are no default privileges. A new table is invisible to the application until `db/grants.psql` lists it, so access is never granted by accident. The file revokes everything first and then grants, so it can be re-run safely.
- The menu is read only for the application, so a compromised bot cannot change prices. Order lines and the update ledger are insert only, so recorded history cannot be rewritten.
- `PUBLIC` loses `CONNECT`, so other roles on the server cannot open the database.

Connection string.
- `DATABASE_URL` carries no `sslmode`. The driver then uses `prefer`, which is acceptable on loopback. A database reached over a network should use `sslmode=verify-full` with a root certificate.

## Consequences

- Run migrations as the owner: `docker exec -i omnipos-db psql -U omnipos_owner -d omnipos < migrations/001_orders_status_check.sql`.
- `db/initdb/` runs only when the data directory is empty. Existing databases move forward through numbered files in `migrations/`.
- The role script creates the role once. It fails on a second run instead of silently resetting grants.
- After any migration that adds a table, re-run the grants as the owner: `docker exec -i omnipos-db psql -U omnipos_owner -d omnipos -v app_user=omnipos_app < db/grants.psql`.

## Revisit when

- Production hosting is chosen. A managed PostgreSQL service replaces the container and the init scripts, but the two role model carries over.
- PostgreSQL 19 is released and 18 needs an upgrade plan (dump and restore, or `pg_upgrade`).
