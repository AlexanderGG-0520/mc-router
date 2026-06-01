# Architecture

## Goal

`mc-router` is a standalone Minecraft Java Edition gateway. It accepts TCP connections on one public listener, parses the first Minecraft handshake packet, reads the requested server address, selects a backend, forwards the original handshake bytes, then proxies TCP in both directions.

The design is intentionally smaller than a Kubernetes controller. Static routes come first. Kubernetes discovery feeds the same route snapshot model through route providers, while wake-up and policy control can be added as separate admission components later.

## Initial Components

- `cmd/mc-gateway`: binary entry point, flags, config loading, signal handling.
- `internal/config`: YAML parsing, defaults, validation.
- `internal/mcproto`: Minecraft VarInt and handshake parsing.
- `internal/router`: normalized hostname to backend selection.
- `internal/proxy`: listener, per-connection handling, backend dial, TCP copy.
- `internal/metrics`: Prometheus registry, low-cardinality proxy metrics, and optional HTTP metrics server.
- `internal/logging`: structured JSON logger setup.

## Connection Flow

1. Client connects to `mc-gateway` on TCP `25565`.
2. Gateway sets a handshake read deadline.
3. Gateway reads only the first Minecraft packet, capped by configured parser limits.
4. Gateway parses:
   - packet length
   - packet id
   - protocol version
   - server address
   - server port
   - next state
5. Gateway normalizes the route address to lowercase and trims a trailing dot.
6. Gateway selects a backend from static config.
7. Gateway dials the backend with a timeout.
8. Gateway writes the exact original handshake bytes to the backend.
9. Gateway proxies the remaining TCP stream both ways.

## Proxy Lifecycle

The MVP uses a deliberately conservative TCP lifecycle:

1. Handshake read is bounded by packet limits and `handshakeTimeout`.
2. Route lookup happens before any backend dial.
3. Unknown or invalid routes are denied without calling the backend dialer.
4. Backend dial uses the request context plus `backendDialTimeout`.
5. The original handshake bytes are written to the backend before stream proxying starts.
6. Two copy goroutines proxy `client -> backend` and `backend -> client`.
7. When either copy direction finishes first, both connections are closed and both copy goroutines are waited.
8. When the server context is cancelled, active client and backend connections are closed and the handler waits for copy goroutines to exit.

This is stricter than a full transparent TCP proxy that preserves half-closed connections. Minecraft clients and servers do not rely on TCP half-close as an application-level signal in the normal login/play flow, so closing both sides on the first completed direction is simpler and safer for the MVP. It avoids keeping a backend socket open after the client has gone away, and it makes shutdown behavior deterministic. If a later feature needs true half-close preservation, it should be added with explicit integration tests for backend responses after client `CloseWrite`.

Lifecycle log `reason` values are intended to map cleanly to future metrics:

- `client_close`
- `backend_close`
- `backend_dial_failed`
- `backend_dial_timeout`
- `handshake_malformed`
- `handshake_timeout`
- `initial_write_failed`
- `route_denied`
- `context_cancelled`

Known limitations:

- The gateway sends a login-state disconnect packet only for explicitly enabled route denied login fallback.
- Copy close reasons identify the first completed direction; low-level TCP reset details are not classified beyond that.

## Fallback Responses

The gateway can optionally return Minecraft Java Edition fallback responses for selected failures. This is intentionally narrow:

- Route denied decisions are eligible when `respondOnRouteDenied` is enabled.
- Backend dial failures and backend dial timeouts are eligible only for status fallback when `respondOnBackendFailure` is enabled.
- Status fallback only handles handshakes with `next_state=status`.
- Login fallback only handles route denied handshakes with `next_state=login`.
- Malformed handshakes, malformed status requests, oversized packets, invalid VarInts, and unsupported next states are still closed without a friendly response.
- Malformed login start packets are closed without a friendly response.
- Backend failure login fallback, maintenance mode, initial backend write failure fallback, context cancellation fallback, and play-state handling are not implemented yet.

Fallback is disabled by default because answering unknown hosts can give scanners more information than a TCP close. Operators must opt in. Once a specific fallback state is enabled, route denied responses default to enabled for backward compatibility with the first fallback implementation. Backend failure status responses remain separately opt-in because they can reveal that a route exists but its backend is unavailable:

```yaml
fallback:
  enabled: true
  login:
    enabled: false
    respondOnRouteDenied: true
    message: "Server unavailable. Please try again later."
  status:
    enabled: true
    respondOnRouteDenied: true
    respondOnBackendFailure: false
    motd: "Server unavailable"
    protocolName: "mc-gateway"
    protocolVersion: 767
    maxPlayers: 0
    onlinePlayers: 0
```

When enabled for an eligible status failure, the status fallback path reads exactly one status request packet `0x00`, writes a minimal status response JSON, and if the client sends a ping packet `0x01`, echoes its 8-byte payload in a pong packet `0x01`. Reads remain bounded by packet limits and `handshakeTimeout`. The status JSON includes `version.name`, `version.protocol`, `players.max`, `players.online`, and `description` as a JSON chat component. Favicon and player samples are intentionally omitted.

When enabled for an eligible login route denial, the login fallback path reads exactly one login start packet before writing a disconnect. For protocol 767, this implementation expects serverbound login start packet `0x00` with username string plus 16 UUID bytes, and it writes clientbound login disconnect packet `0x00` with a JSON chat component reason. Unsupported protocol versions, malformed login start packets, missing login start packets, and scanner-like input are closed without a friendly response. The username is not logged and is not used as a metric label.

`defaultRoute` still takes precedence over route denied fallback. If `unknownHostPolicy=default` selects a backend and the dial succeeds, the connection is proxied normally. If that selected default backend cannot be dialed and backend failure fallback is enabled, the status fallback response can be returned for status-state clients.

Backend failure fallback happens only for status clients after route selection has matched an explicit route or the default route and the backend dial fails or times out. It does not run for route denied, context cancellation, malformed input, login state, or failed initial writes after a backend connection was established.

Existing route decision and backend dial metrics keep their original meanings. A route denied fallback still records `mc_gateway_route_decisions_total{result="denied"}`. A backend failure fallback records the route decision as `matched` or `default` and records the backend dial failure reason. A successful fallback response increments `mc_gateway_fallback_responses_total{state="<state>",reason="<reason>"}` after the response packet is written. If the required request packet is malformed, the client closes before the response is written, or fallback is disabled, the fallback response counter is not incremented.

## Metrics

Prometheus metrics are exposed by an optional HTTP server. Metrics are disabled by default to preserve the previous runtime surface, avoid hot-path collector work, and avoid exposing an unauthenticated listener unless operators opt in:

```yaml
metrics:
  enabled: true
  listen: ":9090"
  path: "/metrics"
```

When enabled, failure to bind `metrics.listen` fails startup. During shutdown, the metrics HTTP server follows the same process context and shuts down gracefully.

Current metrics are intentionally low-cardinality:

- `mc_gateway_connections_total{result,reason}`
- `mc_gateway_backend_dials_total{result,reason}`
- `mc_gateway_fallback_responses_total{state,reason}`
- `mc_gateway_reload_total{result}`
- `mc_gateway_route_decisions_total{result}`
- `mc_gateway_kubernetes_watch_restarts_total{reason}`
- `mc_gateway_kubernetes_discovery_errors_total{reason}`
- `mc_gateway_kubernetes_skipped_services{reason}`
- `mc_gateway_active_connections`
- `mc_gateway_config_generation`
- `mc_gateway_routes`
- `mc_gateway_kubernetes_watch_running`
- `mc_gateway_kubernetes_last_successful_sync_timestamp_seconds`
- `mc_gateway_kubernetes_discovered_routes`
- `mc_gateway_connection_duration_seconds`
- `mc_gateway_backend_dial_duration_seconds`

Do not add remote address, username, requested server address, backend host, MOTD, protocol version, or raw message labels. Host-level or backend-level metrics should be considered later with an explicit cardinality budget.

Kubernetes discovery metrics follow the same rule. They use bounded `reason` values only and do not include namespace, Service name, host, backend, annotation value, resource version, or raw error text. Metrics are disabled by default; when disabled, discovery instrumentation is a no-op.

Fallback response metric labels are deliberately bounded:

- `state`: `status` or `login`.
- `reason`: `route_denied`, `backend_dial_failed`, or `backend_dial_timeout`.

The fallback response counter tracks responses that were actually written, not fallback handling attempts. Ping/pong completion is not required for status fallback because the status response is already visible to the client at that point.

Lifecycle `reason` values used by logs and metrics are kept aligned:

- `success`
- `unknown`
- `client_close`
- `backend_close`
- `backend_dial_failed`
- `backend_dial_timeout`
- `handshake_malformed`
- `handshake_timeout`
- `initial_write_failed`
- `route_denied`
- `context_cancelled`

`mc_gateway_routes` counts explicit `routes` entries only. The `defaultRoute` is not included. `mc_gateway_config_generation` starts at `1` for the startup config and increments after a successful reload or runtime discovery snapshot update.

## Route Sources

The runtime always supports static YAML routes. When Kubernetes discovery is enabled, startup performs one namespace-scoped Kubernetes Service initial list, builds a Kubernetes discovery `Result`, exposes `Result.Routes` through a Kubernetes `SnapshotProvider`, merges those discovered routes into the initial route snapshot, then starts a namespace-scoped Service watch controller under a retry/backoff supervisor.

Kubernetes Service annotation discovery includes:
- Discovery configuration validation and annotation parsing.
- Pure `BuildDiscoveredRoutes` ServiceInput-to-Result boundary.
- Route-only `RouteProvider` interface and Kubernetes `SnapshotProvider`.
- `RebuildRouteSnapshot` helper for unified static/discovered route management.
- Runtime route snapshot boundary.
- Startup `client-go` Service initial list.
- Runtime Service watch updates that publish complete discovery `Result` replacements.
- Runtime watch retry/backoff supervision.

Static routes take precedence over discovered routes. `defaultRoute` remains outside the explicit route list and is evaluated after static and discovered routes. See [Kubernetes Discovery](kubernetes-discovery.md).

Kubernetes watch updates provide a complete replacement discovery `Result`. The runtime converts `Result.Routes` through `SnapshotProvider`, then rebuilds a route snapshot from the latest valid static config plus that route-only provider. It swaps the active snapshot only if the rebuild succeeds. Skipped Services, duplicate host metadata, and skipped reason counts remain discovery result, logging, and metrics concerns rather than route-provider data. Watch controller failures and rebuild failures keep the previous active snapshot. After the first successful sync, watch failures are retried with backoff by relisting Services and opening a new watch.

Kubernetes discovery metrics are updated only after successful startup snapshot rebuilds, successful runtime syncs, and bounded failure points. Rebuild failures increment a discovery error counter and keep the previous discovered route and skipped Service gauges unchanged. `mc_gateway_kubernetes_skipped_services{reason}` reflects the latest successfully applied discovery snapshot.

Startup discovery builds a complete Kubernetes discovery `Result`, rebuilds the startup route snapshot through the route-only `SnapshotProvider`, creates the server metrics recorder, and then records skipped Service counts from the startup discovery report. That metadata does not flow through `RouteProvider`, `SnapshotProvider`, merge data, or `RouteSnapshot`.

## Config Reload

`mc-gateway` can reload its static YAML config after receiving `SIGHUP` on supported platforms. Reload is limited to rebuilding the route configuration and swapping the active router snapshot.

Reload behavior:

1. The gateway reads the configuration file from its startup path.
2. Static configuration is parsed and validated.
3. Valid static configuration is merged with the latest discovered routes.
4. A new router is built from the merged routes and `defaultRoute`.
5. The active snapshot is replaced atomically only after all previous steps succeed.
6. New connections use the new snapshot; existing connections are not affected.

If reload fails at any step, the gateway logs `reload_failed` and the existing snapshot remains active.

A successful reload logs `reload_success`. The listener address is still a startup setting; changing `listen` in the file requires a restart to bind a different address.

Metrics server settings are also startup settings. SIGHUP reload updates route snapshots and route-related metrics, but changes to `metrics.enabled`, `metrics.listen`, or `metrics.path` require a process restart.

Fallback settings are part of the route config snapshot and are applied to new connections after a successful reload.

Kubernetes discovery does not reconfigure its namespace, client, or annotation prefix during reload. The latest discovered routes from the running watch controller are preserved and re-merged with the new static config. If the watch supervisor is retrying while reload happens, reload still uses the latest successful discovered route set.

The implementation uses an immutable snapshot stored behind an atomic pointer. That keeps the per-connection hot path small: each connection loads one snapshot, then uses that config and router for deadlines, route selection, and backend dialing. It avoids holding a mutex while clients connect or while backend dials are in progress, and the race detector covers concurrent reload plus active connections.

Reload and Kubernetes watch updates are serialized only while building and swapping snapshots. Active connections keep using the snapshot they loaded at connection start. New connections use the latest successfully swapped snapshot.

Future route sources should feed the same router model rather than rewriting the proxy path:

- Kubernetes Service labels or annotations.
- Namespace scoped discovery.
- CRD based route objects.
- Generated route cache from a controller.

## Kubernetes Direction

Minecraft workloads are expected to live in their own namespaces. The gateway can stay in a dedicated namespace and route to service DNS names such as:

```text
alec-smp.alec-smp.svc.cluster.local:25565
```

With Cilium and CRI-O, the gateway should rely on normal Kubernetes Service networking first. Direct Pod routing, endpoint awareness, or Cilium-specific policy integration should be added only when a concrete need appears.

## Future Wake-Up Controller

Wake-up should not be mixed into the handshake parser. A later controller can add a decision step between route selection and backend dial:

1. Route match identifies desired backend or workload.
2. Wake-up controller checks readiness or scale state.
3. If sleeping, it triggers scale-up and optionally routes to fallback.
4. Gateway either waits within a bounded timeout or returns a controlled disconnect.

That keeps the MVP proxy deterministic and testable.
