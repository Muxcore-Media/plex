# Plex Playback Bridge

Plex Media Server bridge for MuxCore playback monitoring (Tautulli / Tracearr Phase 2).

## Capabilities

- `playback.plex` — Plex session source
- `playback` — publishes `playback.started` / `playback.progress` / `playback.stopped` mesh events
- `settings`

## Environment

| Variable | Default | Description |
|----------|---------|-------------|
| `PLEX_URL` | — | Plex server base URL (e.g. `http://plex:32400`) |
| `PLEX_TOKEN` | — | Plex authentication token |
| `PLEX_DATA_DIR` | `/var/lib/muxcore-plex` | Durable settings (`settings.json`) and catalog sync state |
| `PLEX_HTTP_SECRET` | — | Shared secret for `GET /sync-lists` (`X-Plex-Bridge-Secret` header). Empty → 401 |
| `PLEX_SESSIONS_POLL_SEC` | `30` | Poll interval for `/status/sessions` (fallback when SSE disconnected) |
| `PLEX_SSE` | on | Connect to Plex `/:/eventsource/notifications` |
| `PLEX_SYNC_LIST_POLL_SEC` | `3600` | Refresh interval for Plex.tv sync lists |
| `PLEX_CATALOG_SYNC_SEC` | `21600` | Library catalog mesh sync interval |
| `PLEX_GRPC_ADDR` | `:9476` | gRPC listen address |
| `PLEX_HTTP_ADDR` | `:8476` | HTTP health + sync-list API |
| `MUXCORE_GRPC_ADDR` | — | Core mesh for event publish |

Copy `.env.example` for a local template.

## HTTP / gRPC

- `GET /healthz` — calls `Health()`; 503 when unconfigured or `/identity` fails
- `GET /sync-lists?user_id=&client_id=&refresh=1` — cached Plex.tv sync lists (requires `X-Plex-Bridge-Secret`)
- gRPC `ListSyncLists` — same data for admin/automation
- gRPC `ListSessions` — active Plex sessions (id, user, item, position, paused, device)
- gRPC `Status` — configured, base URL, active sessions, `sse_connected`, `machine_id`
- gRPC `PlayURL` — Plex web deep-link `…/web/index.html#!/server/{machineID}/details?key=/library/metadata/{ratingKey}`
- gRPC `TerminateSession` — stop a Plex session (guard integration)

Connection knobs (`plex_url`, `plex_token`, poll interval, `plex_sse`) persist in `{PLEX_DATA_DIR}/settings.json` and survive restarts.

Events are consumed by `playback-monitor` for history/analytics and `playback-guard` for rule evaluation.
