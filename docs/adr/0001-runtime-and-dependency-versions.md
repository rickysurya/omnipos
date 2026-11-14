# 0001: Runtime and dependency versions

Status: accepted, 2026-11-12

## Context

The project should run on supported software only. Go and PostgreSQL both publish support policies, which this record checks against as of the date above. Neither project uses an "LTS" label: Go supports the two newest majors, and PostgreSQL supports every major for five years.

## Decision

Go: `go 1.27.4` in `go.mod`.
- Go supports each major release until two newer majors exist. As of this record the supported majors are 1.26 and 1.27.
- 1.27 has the longest remaining support, so a new project starts on it.
- 1.27.4 and 1.26.11 were released on 2026-11-10 with security fixes in `net/http`, which this server uses. The earlier pin of 1.26.10 was one patch behind and missed those fixes.
- Go 1.27 moves `encoding/json` to a stricter implementation (it rejects invalid UTF-8 and duplicate object keys). Telegram sends well formed JSON, so the webhook decoder is unaffected.

PostgreSQL: major version 18. See ADR 0002 for the image and reasoning.

Libraries stay on the versions recorded in `go.sum`: chi v5.5.0, pgx v5.12.0, gorilla/websocket v1.5.3, godotenv v1.5.1. I could not confirm the newest release of each from the environment this was prepared in, so verify on a machine with network access:

```
go list -m -u all
govulncheck ./...
```

## Library notes

- chi: `middleware.RealIP` is not used. It trusts client supplied headers, and chi's own v5.3.0 release notes describe spoofing advisories against it. The router never needs the client address for a decision.
- gorilla/websocket: kept at v1.5.3, the latest release on its releases page. Its v1.4.2 notes say the project is still looking for maintainers, and there is no archival notice. The library is used in one file (`websocket.go`) behind the `KitchenUpdater` and `Broadcaster` ports, so replacing it is a contained change. I did not evaluate coder/websocket, so there is no evidence here that it is better.

## Update policy

- Apply Go patch releases and PostgreSQL minor releases when published. Both projects recommend running the current patch or minor.
- Review dependency updates monthly and run `govulncheck` in CI.

## Revisit when

- Go 1.29 ships (Go 1.27 leaves support).
- gorilla/websocket is archived or has an advisory with no fix.
