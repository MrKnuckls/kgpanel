# KGpanel v1 — architecture notes

Clean-room Go implementation of the panel + daemon pattern. Original code (MIT),
not derived from Pelican/Pterodactyl (AGPL) — the AI-VM brief forbids forking the
panel; wings-style daemon design is fair game but ours is written fresh.

## Components

### panel (kgpanel binary)
- `internal/config` — flags + env (`KGPANEL_*`)
- `internal/store` — JSON-file persistence (users/nodes/eggs/servers/audit),
  atomic tmp+rename writes; package boundary ready for a SQL backend later
- `internal/auth` — bcrypt password hashing, HMAC-SHA256 signed tokens (30-day)
- `internal/api` — REST + websocket proxy, bearer-token middleware
- `internal/wingsclient` — HTTP client toward wings daemons (node token header)
- `internal/web` — embedded static UI (branded knuckls palette)

### wings (wings binary)
- `internal/docker` — docker CLI wrapper: create/start/stop/restart/kill,
  status, log streaming, exec
- `internal/api` — `/v1/servers/{id}/{power,status,console}` + `/v1/health`,
  shared-token auth

## API surface (v1)

| Route | Method | Notes |
|---|---|---|
| /api/setup | POST | one-time admin bootstrap (409 after first) |
| /api/login | POST | returns bearer token |
| /api/me | GET | profile |
| /api/nodes, /api/nodes/create | GET/POST | wings registration |
| /api/eggs, /api/eggs/create | GET/POST | server templates |
| /api/servers, /api/servers/create | GET/POST | server CRUD |
| /api/servers/{id}/power | POST | start/stop/restart/kill |
| /api/servers/{id}/status | GET | real state from wings |
| /api/console/{id} | WS | proxied to wings |
| /api/audit?limit=N | GET | audit trail |
| /v1/servers/{id}/power | POST | wings side |
| /v1/servers/{id}/status | GET | wings side |
| /v1/servers/{id}/console | WS | wings side |
| /v1/health | GET | wings side |

## Container model

One docker container per server, named `kg-<serverID>`, memory + cpu-quota
limits set at create time, `--restart unless-stopped`. Ports published
host-side as given in the egg (`25565/tcp` etc). Console = `docker logs -f`,
input = `docker exec -i <ctr> /bin/sh -c cat`.

## Security notes

- Wings trusts the shared node token (per-node secret generated when the node
  is created in the panel). Panel proxies console WS to wings carrying the node
  token — the browser never sees it.
- Audit records actor + action + target for logins, failures, power, CRUD.
- `kill` is deliberately not offered in the UI (hard kill should be deliberate).
- Behind Cloudflare Access in production (like the existing kgpanel).

## v2 candidates
- File manager via wings (tar streaming), install scripts per egg, backups,
  scheduler/cron, Postgres storage, node resource graphs.
