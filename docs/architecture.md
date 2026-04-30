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
- There is no per-connection byte or duration metric yet.

## Route Sources

The MVP has a single route source: static YAML.

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
