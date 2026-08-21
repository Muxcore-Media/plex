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
| `PLEX_SESSIONS_POLL_SEC` | `30` | Poll interval for `/status/sessions` |
| `PLEX_SSE` | on | Connect to Plex `/:/eventsource/notifications` |
| `PLEX_SYNC_LIST_POLL_SEC` | `3600` | Refresh interval for Plex.tv sync lists |
| `PLEX_CATALOG_SYNC_SEC` | `21600` | Library catalog mesh sync interval |
| `PLEX_GRPC_ADDR` | `:9476` | gRPC listen address |
| `PLEX_HTTP_ADDR` | `:8476` | HTTP health + sync-list API |
| `MUXCORE_GRPC_ADDR` | — | Core mesh for event publish |

## HTTP / gRPC

- `GET /sync-lists?user_id=&client_id=&refresh=1` — cached Plex.tv sync lists (Tautulli `get_sync_lists` parity)
- gRPC `ListSyncLists` — same data for admin/automation
- gRPC `TerminateSession` — stop a Plex session (guard integration)

Events are consumed by `playback-monitor` for history/analytics and `playback-guard` for rule evaluation.
