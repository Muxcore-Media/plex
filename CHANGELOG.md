# Changelog

## [Unreleased]

### Security
- Outbound Plex and plex.tv URLs go through netguard Integration (private LAN and loopback allowed; link-local, cloud metadata, and non-HTTP schemes refused). Settings reject a blocked base URL. The notification SSE dial uses the same guard (NFR-SEC-009).

## [0.1.5] - 2026-10-05


### Security
- gRPC server and peer dials use mesh TLS (meshtls, sdk/go/module v0.6.5) unless the dev insecure flag is set (ADR-0016/0017).

## [0.1.4] - 2026-10-05

### Changed
- Built on core v0.6.14 / sdk/go/module v0.6.4: unregisters on shutdown and re-registers after core restarts (ADR-0022).

## [0.1.3] - 2026-10-05


### Changed
- Reported version comes from muxcore.json (ADR-0021); built on core v0.6.12 / sdk/go/module v0.6.3 (mesh enrollment, ADR-0017).

## [0.1.2] - 2026-10-05

