# MVP

## Included

- Go single binary named `mc-gateway`.
- Static YAML route config.
- TCP listener.
- Minecraft Java Edition handshake parser.
- Route lookup by requested `serverAddress`.
- Backend TCP proxy.
- Unknown host `deny` or `default` policy.
- JSON structured logging.
- Optional Prometheus metrics endpoint.
- Optional status ping fallback response for denied routes and backend dial failures.
- Optional login disconnect fallback response for denied login starts.
- Fallback response metrics for status and login fallback responses.
- Low-cardinality Kubernetes discovery metrics.
- Graceful shutdown.
- SIGHUP config reload for static route updates and Kubernetes Service re-list on supported platforms.
- Unit tests for parser, config, and router behavior.
- Dockerfile.
- Minimal Kubernetes manifest and namespace-scoped discovery RBAC example.
- GitHub Actions CI.
- Kubernetes discovery config, current namespace helper, Service annotation parser, in-memory controller core, merge-builder, internal provider interface, memory provider, startup client-go initial list, SIGHUP reload Service re-list, namespace-scoped Service watch controller, watch retry/backoff supervisor, namespace-scoped RBAC example, runtime merge-boundary integration, runtime route snapshot updates, and discovery metrics.

## Not Included Yet

- Configurable retry/backoff settings for runtime Kubernetes watch failures.
- ClusterRole or all-namespaces discovery RBAC.
- Pod annotations, EndpointSlice discovery, or CRD route discovery.
- Scale-to-zero wake-up.
- Backend failure login fallback and maintenance fallback responses.
- UDP or extra TCP routing for mods such as Simple Voice Chat.
- REST or admin API.
- Web UI.
- Filesystem watch or admin-triggered dynamic reload.
- Per-route rate limits or allow/deny lists.

## MVP Acceptance Checks

The MVP is acceptable when these commands pass:

```powershell
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
docker build -t mc-gateway:dev .
```

Manual network acceptance should use at least two backend Minecraft servers and two DNS names pointing at the gateway. The gateway should route each client based on the server address entered in the Minecraft client.

## Connection Flow

1. Client opens a TCP connection to `mc-gateway`.
2. `mc-gateway` applies a handshake read deadline.
3. `mc-gateway` reads exactly the first Minecraft packet, bounded by VarInt and packet length limits.
4. `mc-gateway` parses the handshake and normalizes the requested server address.
5. The router selects an explicit route, a default route, or denies the connection.
6. For allowed routes, `mc-gateway` dials the selected backend with a timeout.
7. `mc-gateway` writes the original handshake bytes to the backend before proxying the rest of the stream.
8. Bidirectional TCP copy runs until one side closes or the server context is cancelled.
9. Both connections are closed and copy goroutines are waited before the connection handler returns.

## Verified Behavior

Automated tests now cover:

- VarInt decoding, including malformed overlong VarInts.
- Handshake parsing for valid login handshakes.
- Rejection of oversized, truncated, unsupported packet id, invalid next state, invalid hostname, and too-long hostname handshakes.
- Static config validation for unknown fields, invalid policy, duplicate route, invalid backend, invalid route hostname, and missing default backend.
- Route matching for case-insensitive hostnames and trailing dots.
- Unknown host deny behavior.
- Default route behavior.
- Fake-backend TCP proxy integration where the backend receives the original handshake packet plus remaining client bytes.
- Malformed handshake denial without connecting to the backend.
- Idle client handshake read timeout.
- Server shutdown on context cancellation.
- SIGHUP/static config reload keeps the previous routes after invalid reloads and applies valid reloads to new connections.
- Kubernetes Service annotation discovery with fake clients, including current namespace resolution, initial list failure policy, Service watch updates, invalid annotation skips, duplicate discovered host handling, static route precedence, ExternalName skips, watch retry/backoff recovery, last-known-good snapshot preservation during retry, Service re-list during reload, and watch update interaction with static reload.
- Metrics config defaults, Prometheus text endpoint behavior, active connection gauge, route-denied counter, backend dial failure counter, fallback response counter, reload counters, config generation gauge, route count gauge, and Kubernetes discovery metric output.
- Status fallback config defaults, denied status response JSON, backend dial failure and timeout status response JSON, status ping/pong echo, disabled fallback close behavior, default route precedence, malformed status request close behavior, fallback response metrics, and existing metric preservation.
- Login fallback config defaults, denied login disconnect JSON, malformed login start close behavior, known route/default route precedence, backend dial failure close behavior, and login fallback response metrics.
- Race detector coverage through `go test -race ./...`.
- A short fake-backend soak test with concurrent connections to exercise connection open, proxy copy, close, and shutdown paths.
- Minecraft protocol smoke tests using lightweight fake backends:
  - Status flow: handshake with next state `status`, status request, JSON status response, ping request, and pong response through the router.
  - Login start flow: handshake with next state `login`, login start packet, and login disconnect packet through the router.
- Optional real Minecraft server E2E smoke test:
  - Manual GitHub Actions workflow only.
  - Uses a real Paper server via Docker and verifies status plus login-start traffic through the gateway.
  - See [docs/e2e.md](e2e.md).

The soak test is intentionally small. It is meant to catch obvious lifecycle regressions, data races, and stuck connection handlers during CI. It is not a high-load benchmark, not a capacity test, and not a substitute for an end-to-end test against real Minecraft clients and servers.

The protocol smoke tests use fixed protocol framing helpers and fake TCP backends. They verify that the router preserves the packet stream across state transitions that real clients use first: status and login start. They are not a full protocol compatibility suite. They do not validate encryption, compression, play state packets, or modded protocol extensions. The optional real-server E2E smoke test covers the first status and login-start responses from a real Paper server, but still does not complete encrypted login or play state.

## Next Implementation Priorities

1. Add configurable Kubernetes watch retry/backoff settings if operators need to tune reconnect behavior.
2. Add wake-up controller behavior for scaled-to-zero servers.
3. Add backend failure login fallback if operators need a login-state disconnect for unavailable known routes.
4. Add CRD only after static and label based models are stable.
5. Consider filesystem watch or admin-triggered reload if SIGHUP is not enough operationally.
