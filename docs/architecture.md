# Architecture

## Goal

`mc-router` is a standalone Minecraft Java Edition gateway. It accepts TCP connections on one public listener, parses the first Minecraft handshake packet, reads the requested server address, selects a backend, forwards the original handshake bytes, then proxies TCP in both directions.

The design is intentionally smaller than a Kubernetes controller. Static routes come first. Kubernetes discovery, wake-up, and policy control can be added as route providers or admission components later.

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

- The gateway does not send Minecraft protocol disconnect packets yet.
- Copy close reasons identify the first completed direction; low-level TCP reset details are not classified beyond that.

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
- `mc_gateway_reload_total{result}`
- `mc_gateway_route_decisions_total{result}`
- `mc_gateway_active_connections`
- `mc_gateway_config_generation`
- `mc_gateway_routes`
- `mc_gateway_connection_duration_seconds`
- `mc_gateway_backend_dial_duration_seconds`

Do not add remote address, username, requested server address, or backend host labels. Host-level or backend-level metrics should be considered later with an explicit cardinality budget.

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

`mc_gateway_routes` counts explicit `routes` entries only. The `defaultRoute` is not included. `mc_gateway_config_generation` starts at `1` for the startup config and increments only after a successful reload.

## Route Sources

The MVP has a single route source: static YAML.

## Config Reload

`mc-gateway` can reload the static YAML config after receiving `SIGHUP` on platforms that support that signal. Reload is intentionally limited to rebuilding the validated route config and swapping the active router snapshot. It does not add a REST API, admin API, Web UI, filesystem watcher, or Kubernetes API watch.

Windows local development does not use `SIGHUP`; restart the process after config changes there. A future admin command can cover platforms where process signals are awkward.

Reload behavior:

1. The gateway reads the same config file path that was used at startup.
2. The normal config parser and validation run.
3. A new router is built from the validated config.
4. The active config/router snapshot is replaced atomically only after all previous steps succeed.
5. New connections use the new snapshot.
6. Active connections keep using the snapshot they selected at connection start and are not disconnected by reload.

If reload fails because the file cannot be read, YAML is malformed, validation fails, or router construction fails, the existing snapshot stays active and the gateway logs `reload_failed`. A successful reload logs `reload_success`. The listener address is still a startup setting; changing `listen` in the file requires a restart to bind a different address.

Metrics server settings are also startup settings. SIGHUP reload updates route snapshots and route-related metrics, but changes to `metrics.enabled`, `metrics.listen`, or `metrics.path` require a process restart.

The implementation uses an immutable snapshot stored in `atomic.Value`. That keeps the per-connection hot path small: each connection loads one snapshot, then uses that config and router for deadlines, route selection, and backend dialing. It avoids holding a mutex while clients connect or while backend dials are in progress, and the race detector covers concurrent reload plus active connections.

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
