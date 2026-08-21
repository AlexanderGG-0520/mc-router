# mc-router

![Docker Build](https://img.shields.io/github/actions/workflow/status/AlexanderGG-0520/mc-router/docker-publish.yml?branch=main)
[![Docker Pulls](https://img.shields.io/docker/pulls/alecjp02/mc-router.svg?logo=docker)](https://hub.docker.com/r/alecjp02/mc-router/)
[![Docker Stars](https://img.shields.io/docker/stars/alecjp02/mc-router.svg?logo=docker)](https://hub.docker.com/r/alecjp02/mc-router/)
[![GitHub Issues](https://img.shields.io/github/issues-raw/alexandergg-0520/mc-router.svg)](https://github.com/alexandergg-0520/mc-router/issues)
![GHCR](https://img.shields.io/badge/GHCR-ghcr.io%2Falexandergg--0520%2Fmc--router-blue)
![License](https://img.shields.io/badge/license-MIT-green)
![Kubernetes](https://img.shields.io/badge/kubernetes-ready-blue)

`mc-router` is a Go-based Minecraft Java Edition gateway for routing a single public TCP entry point, usually `:25565`, to multiple backend Minecraft servers based on the hostname that the client requested in the Minecraft handshake.

The first binary is named `mc-gateway` so the project can be renamed later without changing the initial command layout.

## Name Candidates

- `mc-gateway`
- `mc-router-k8s`
- `mine-gateway`
- `block-gateway`
- `mc-ingress-router`

For now the repository remains `mc-router` and the binary remains `mc-gateway`.

## Why This Is Separate From the Minecraft Server Image

The Minecraft server image should stay focused on running one server process and its server-side lifecycle. Routing, connection admission, Kubernetes discovery, future wake-up control, fallback behavior, rate limiting, and metrics are gateway responsibilities. Keeping those responsibilities separate makes each Minecraft workload simpler, lets namespaces remain isolated, and allows the gateway to evolve without rebuilding every server image.

## MVP Scope

Implemented in this skeleton:

- TCP listener for Minecraft Java Edition connections.
- Minecraft handshake parser with VarInt support, including Minecraft Java 1.20.5+ transfer intent.
- Requested `serverAddress` based static route matching.
- TCP proxying to selected backend `host:port` for Login and Transfer traffic.
- `statusBackend` routes use router-terminated STATUS backed by an independently maintained health state:
  configurable consecutive probe outcomes determine NORMAL or DEGRADED, and a
  stopped observation loop fails closed.
- Unknown host deny policy, with optional default route policy.
- Optional fallback responses for denied status pings and denied login starts.
- Structured JSON logging through Go `log/slog`.
- Prometheus metrics endpoint when explicitly enabled, including low-cardinality fallback response counters.
- Optional fixed-backend UDP relay foundation for local transport validation.
- Optional Bedrock Edition UDP entrypoint forwarding to one Geyser-enabled backend.
- Handshake read timeout and backend dial timeout.
- Graceful shutdown on SIGINT/SIGTERM.
- Unit tests for VarInt, handshake parsing, config loading, and route matching.
- Dockerfile and minimal Kubernetes manifest.
- GitHub Actions CI for `gofmt`, `go test`, `go vet`, and Docker build smoke.
- Namespace-scoped Kubernetes Service annotation discovery, including startup initial list, `SIGHUP` reload re-list, runtime watch updates, retry/backoff recovery, and low-cardinality discovery metrics.

Deferred by design:

- Kubernetes discovery beyond namespace-scoped Service annotations, such as Pod annotations, EndpointSlice discovery, CRDs, all-namespaces RBAC, or informer-based implementation.
- Scale-to-zero wake-up and scale-down control.
- Maintenance fallback behavior.
- REST API.
- Web UI.
- CRD definitions and controllers.

## Transfer-Aware Simple Voice Chat Routing

`mc-router` includes an experimental dynamic UDP routing mode for [Simple Voice Chat](https://modrinth.com/plugin/simple-voice-chat).

Unlike the fixed-backend UDP relay, this mode can route a player's Simple Voice Chat UDP traffic to the backend Minecraft server that currently owns that player, including after a Minecraft Transfer.

The architecture consists of:

* the Go-based `mc-gateway`, which owns the shared public UDP listener and the authenticated registration API;
* a Paper or Fabric companion JAR installed on every participating backend;
* one configured backend ID and authentication token per Minecraft backend.

The companion registers a player's UUID with `mc-router` before the Simple Voice Chat UDP endpoint is sent to the client. Registrations use short-lived leases and are refreshed while the player remains connected. Replacing a registration closes stale UDP sessions so late replies from the previous backend are not forwarded after Transfer.

Unknown, expired, malformed, or ambiguous sessions fail closed. `mc-router` does not decrypt, inspect, or log voice payloads or Simple Voice Chat secrets.

## Hub Control Companion

`mc-router-control-companion-paper` is a separate Paper plugin for a Hub. It listens only on a configured internal address and accepts authenticated `PUT /v1/backends/{backendId}/availability` requests with `{"availability":"online"}` or `{"availability":"offline"}`. Backend IDs are an explicit allow-list, request bodies are bounded, and an update emits a Paper `BackendAvailabilityChangeEvent` only when the availability state changes. It does not require Simple Voice Chat.

Set `MC_ROUTER_CONTROL_LISTEN`, `MC_ROUTER_CONTROL_TOKEN`, and a comma-separated `MC_ROUTER_CONTROL_BACKENDS` allow-list. Do not expose its listener outside the Kubernetes cluster. The mc-router availability notifier and any Hub GUI/NPC plugin use this API and event.

## Backend Availability Notifications

Set `availability.enabled: true` to have `mc-gateway` send state changes to the Hub control companion. The gateway performs a Java STATUS handshake and status request for each explicit `availability.backends` entry; a bare successful TCP connection does not count as online. The initial observed state is sent, then only changes are sent. Failed notifications are retried on the next interval. Keep `tokenEnv` in a Kubernetes Secret rather than putting the token in the ConfigMap.

```yaml
availability:
  enabled: true
  interval: "10s"
  timeout: "3s"
  controlURL: "http://mc-router-control.mc-hub.svc.cluster.local:8082"
  tokenEnv: "MC_ROUTER_CONTROL_TOKEN"
  backends:
    - id: "alec-smp-2"
      address: "fabric-c2me-gpu-not-true-crafter-mode.fabric-c2me-gpu-not-true-crafter-mode.svc.cluster.local:25565"
      serverAddress: "c2me.alec-ofc.com"
```

### Current status

Dynamic Simple Voice Chat routing is experimental and has not yet been declared production-ready.

The following are implemented:

* authenticated backend registration;
* bounded, expiring player-to-backend leases;
* UUID-based client UDP routing;
* backend reassignment after Transfer;
* stale-session closure;
* same-IP client isolation;
* Paper and Fabric companion JARs;
* unit and local transport tests.

The following still require real-client validation:

* the exact Simple Voice Chat event order during Minecraft Transfer;
* voice reconnection behavior after Transfer;
* sustained multi-client operation;
* backend and gateway restart behavior;
* deployment behind the intended public UDP endpoint.

A brief interruption in voice connectivity during Transfer is expected. Seamless audio continuity is not currently guaranteed.

### Companion JARs

Build both platform-specific companion JARs with:

```bash
gradle -p companion :fabric:jar :paper:jar
```

Generated artifacts:

```text
companion/fabric/build/libs/mc-router-voicechat-companion-fabric-<version>.jar
companion/paper/build/libs/mc-router-voicechat-companion-paper-<version>.jar
```

Install exactly one companion JAR on each backend:

* Fabric: place the Fabric JAR in `mods/`;
* Paper: place the Paper JAR in `plugins/`.

Simple Voice Chat must be installed separately on every backend. The companion does not bundle or redistribute Simple Voice Chat.

### Required companion configuration

The companion currently reads its configuration from environment variables.

| Variable                                  | Required | Description                                                                 |
| ----------------------------------------- | -------- | --------------------------------------------------------------------------- |
| `MC_ROUTER_VOICECHAT_REGISTRATION_URL`    | Yes      | Internal registration API base URL, such as `http://mc-router:9091`         |
| `MC_ROUTER_VOICECHAT_BACKEND_ID`          | Yes      | Backend ID matching one entry under `voiceChat.backends`                    |
| `MC_ROUTER_VOICECHAT_TOKEN`               | Yes      | Authentication token assigned to that backend                               |
| `MC_ROUTER_VOICECHAT_PUBLIC_HOST`         | Yes      | Public Simple Voice Chat endpoint sent to clients, including the UDP port   |
| `MC_ROUTER_VOICECHAT_INSTANCE_ID`         | No       | Stable owner ID for this server instance; generated at startup when omitted |
| `MC_ROUTER_VOICECHAT_TTL`                 | No       | Local lease timing basis as an ISO-8601 duration; defaults to `PT30S`       |
| `MC_ROUTER_VOICECHAT_REFRESH_INTERVAL`    | No       | Lease refresh interval; defaults to half of the configured local TTL        |
| `MC_ROUTER_VOICECHAT_REQUEST_TIMEOUT`     | No       | Registration HTTP timeout; defaults to `PT5S`                               |
| `MC_ROUTER_VOICECHAT_MAX_TRACKED_PLAYERS` | No       | Maximum locally tracked players; defaults to `4096`                         |

The registration API must remain on a trusted internal network. Do not expose backend authentication tokens or the registration listener to the public internet.

### Compatibility

The table below describes the versions currently targeted by the source tree. It is not a promise of compatibility with unlisted versions.

| Component                      | Fabric companion | Paper companion                      | Validation status                                                                             |
| ------------------------------ | ---------------- | ------------------------------------ | --------------------------------------------------------------------------------------------- |
| Minecraft                      | `26.2`           | `1.21.8`                             | Builds successfully; cross-version Transfer environment still requires real-client validation |
| Java                           | 25               | 21                                   | Compile and JAR packaging verified                                                            |
| Fabric Loader                  | `>=0.19.3`       | N/A                                  | Matches the Simple Voice Chat 2.6.21 + Minecraft 26.2 upstream target                         |
| Simple Voice Chat runtime      | `>=2.6.18`       | Version compatible with Paper 1.21.8 | API compilation and packaging verified; runtime event behavior still requires validation      |
| Simple Voice Chat API artifact | `2.6.20`         | `2.6.20`                             | Compile-time dependency only                                                                  |
| Companion                      | `0.2.0`          | `0.2.0`                              | Experimental, unreleased                                                                      |

The inspected Simple Voice Chat upstream target uses Minecraft `26.2`, Java 25, Fabric Loader `0.19.3`, and Simple Voice Chat `2.6.21+26.2`. The companion compiles against the separately published `voicechat-api:2.6.20` artifact. Runtime compatibility must be validated before the first stable companion release.

See [docs/voicechat-routing-design.md](docs/voicechat-routing-design.md) for the protocol investigation, threat model, rejected alternatives, and session lifecycle.

## Bedrock UDP Forwarding

`mc-router` can expose one public Minecraft Bedrock Edition UDP entrypoint, normally UDP `19132`, and send Bedrock players to Geyser-enabled backends.

Two Bedrock modes are available:

* `udp-forward`: opaque UDP forwarding to `bedrock.defaultBackend`. This preserves the original low-level relay behavior and does not parse RakNet or Bedrock packets.
* `host-proxy`: Bedrock-aware routing using gophertunnel. `mc-router` accepts the Bedrock login, reads the client's requested `ServerAddress`, matches it against `bedrock.routes[].hosts`, then connects to the selected Geyser backend. Unknown hosts fall back to `bedrock.defaultBackend`.

Run Geyser on every backend Minecraft server or backend service that should accept Bedrock players. `mc-router` still does not run Geyser and does not translate Bedrock to Java itself.

The intended design is one public UDP `19132` listener rather than exposing one public UDP port per backend server. Java Edition TCP routing on `listen`, normally TCP `25565`, is separate and unchanged.

`host-proxy` terminates and re-originates the Bedrock session, so it has a larger compatibility surface than `udp-forward`. It depends on gophertunnel tracking current Minecraft Bedrock protocol versions. Backend authentication settings must allow the proxied Bedrock connection model used by the deployment; validate this with the target Geyser/Floodgate configuration before relying on it in production.

```yaml
listen: ":25565"
bedrock:
  enabled: true
  mode: "host-proxy"
  listen: ":19132"
  defaultBackend: "mc-hub.mc-hub.svc.cluster.local:19132"
  sessionTimeout: "30s"
  routes:
    - name: hub
      hosts:
        - "play.example.com"
        - "hub.play.example.com"
      backend: "mc-hub.mc-hub.svc.cluster.local:19132"
    - name: creative
      hosts:
        - "creative.play.example.com"
      backend: "mc-creative.mc-creative.svc.cluster.local:19132"
    - name: survival
      hosts:
        - "survival.play.example.com"
      backend: "mc-survival.mc-survival.svc.cluster.local:19132"
```

## Config

Static YAML is the first supported route source:

```yaml
listen: ":25565"
handshakeTimeout: "5s"
backendDialTimeout: "5s"
clientPolicy:
  # Disabled when both lists are empty. allow takes precedence over deny.
  allow: []
  deny: []
clientRateLimit:
  # Disabled by default. Limits each source IP independently.
  enabled: false
  connectionsPerSecond: 1
  burst: 3
  idleTimeout: "10m"
  maxEntries: 4096
proxyProtocol:
  # Accept headers only from an explicitly trusted TCP peer. Empty by default.
  trustedProxies: []
scalerWebhook:
  # Disabled by default. Notification failure does not block backend dialing.
  enabled: false
  url: "http://scaler.default.svc.cluster.local/wake"
  timeout: "2s"
  headers: {}
configReload:
  # Disabled by default. Detects direct-file writes and Kubernetes ConfigMap updates.
  watch: false
  debounce: "1s"
metrics:
  enabled: true
  listen: ":9090"
  path: "/metrics"
udpRelay:
  enabled: false
  listen: ":24454"
  backend: "hub:24454"
  idleTimeout: "30s"
  backendDialTimeout: "5s"
  maxSessions: 4096
  maxPacketSize: 65535
bedrock:
  enabled: false
  mode: "udp-forward"
  listen: ":19132"
  defaultBackend: "mc-hub.mc-hub.svc.cluster.local:19132"
  sessionTimeout: "30s"
  routes:
    - name: hub
      hosts:
        - "play.example.com"
        - "hub.play.example.com"
      backend: "mc-hub.mc-hub.svc.cluster.local:19132"
    - name: creative
      hosts:
        - "creative.play.example.com"
      backend: "mc-creative.mc-creative.svc.cluster.local:19132"
    - name: survival
      hosts:
        - "survival.play.example.com"
      backend: "mc-survival.mc-survival.svc.cluster.local:19132"
fallback:
  enabled: true
  login:
    enabled: false
    respondOnRouteDenied: true
    message: "Server unavailable. Please try again later."
  status:
    enabled: true
    respondOnRouteDenied: true
    motd: "Server unavailable"
    protocolName: "mc-gateway"
    protocolVersion: 767
    maxPlayers: 0
    onlinePlayers: 0
unknownHostPolicy: "deny"
status:
  probeInterval: "10s"
  probeTimeout: "3s"
  failureThreshold: 3
  recoveryThreshold: 2
  maxObservationAge: "15s"
defaultRoute:
  backend: "lobby.default.svc.cluster.local:25565"
  mode: "allow"
routes:
  - serverAddress: "play.example.com"
    backend: "hub.default.svc.cluster.local:25565"
    statusBackend: "smp.default.svc.cluster.local:25565"
  - serverAddress: "smp.example.com"
    backend: "alec-smp.alec-smp.svc.cluster.local:25565"
    # Optional: answer Java status pings directly without contacting the backend.
    statusOverride:
      motd: "Alec SMP"
      protocolName: "Alec SMP 2"
      protocolVersion: 767
      maxPlayers: 100
      onlinePlayers: 0
  - serverAddress: "lobby.example.com"
    backend: "alec-smp-lobby.alec-smp-lobby.svc.cluster.local:25565"
```

`unknownHostPolicy` supports:

- `deny`: close connections for hosts that do not match an explicit route.
- `default`: send unknown hosts to `defaultRoute.backend`.

`statusBackend` selects the Java Edition STATUS observation source while Login
and Transfer traffic continue to use `backend`. On routes that set it, the
router terminates public STATUS connections, probes the source independently,
and validates its complete response (including extension fields such as favicon
and player samples). Probe results are not public state changes by themselves:
the router applies the configured consecutive-failure and consecutive-success
thresholds to maintain a health state. It is optional; when omitted, STATUS
retains the established transparent proxy behavior through `backend`.

A route can instead set `statusOverride` to return a static Java Edition status response for that hostname. It is used only for the status state: login and Transfer handshakes still proxy to the route's backend. `statusOverride` takes precedence over `statusBackend`; it never starts a source probe. Its `motd`, `protocolName`, `protocolVersion`, `maxPlayers`, and `onlinePlayers` fields are all required; protocol and player counts must be non-negative.

Metrics are disabled by default. Set `metrics.enabled: true` to serve unauthenticated Prometheus text metrics on `metrics.listen` and `metrics.path`. Do not expose this HTTP listener directly to the public internet; it is intended for internal scraping, such as from a Kubernetes cluster Prometheus.

The UDP relay is disabled by default. Set `udpRelay.enabled: true` to bind one UDP listener and forward opaque datagrams bidirectionally to one explicit backend. The relay is a fixed-backend transport foundation only; it does not parse Simple Voice Chat packets, infer backends from TCP routes, or perform Transfer-aware routing.

Bedrock support is disabled by default. Set `bedrock.enabled: true` to bind one UDP listener, usually `:19132`. `bedrock.mode` defaults to `udp-forward`. Use `host-proxy` when hostname-based Bedrock routing is required. Each route host is matched case-insensitively, and host values with ports such as `creative.play.example.com:19132` are normalized to the host. Each backend route must point at a Geyser-enabled Minecraft server or separate Geyser service.

Fallback responses are counted with `mc_gateway_fallback_responses_total{state,reason}` after a fallback response packet is successfully written. Labels are intentionally bounded: `state` is `status` or `login`, and `reason` is one of the documented low-cardinality lifecycle reasons.

For a selected STATUS route, `normal` means: the router's health state is
`NORMAL`, established after `status.recoveryThreshold` consecutive valid source
responses and not subsequently withdrawn by `status.failureThreshold`
consecutive failed source observations. `status.maxObservationAge` forces the
state to be unavailable if the observation loop stops completing probes. All
other states are returned as the configured degraded `fallback.status` response.
Use an explicit unavailable/degraded MOTD; never
reuse the normal MOTD. `fallback.enabled` and `fallback.status.enabled` still
control the optional denied-route response. Set `fallback.login.enabled: true`
to return a protocol 767 login-state disconnect packet for denied login starts.

## Run Locally

```powershell
go run ./cmd/mc-gateway -config examples/config.yaml
```

For local testing with a backend on localhost, edit `examples/config.yaml` to point a route at your test Minecraft server, for example `127.0.0.1:25566`.

## Test

```powershell
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
```

The normal test suite uses fake protocol backends and does not start a real Minecraft server. A separate optional real-server E2E smoke test is available through a manual GitHub Actions workflow and can also be run locally with Docker. See [docs/e2e.md](docs/e2e.md).

Local research and E2E setup for experimental Simple Voice Chat support is documented in [docs/voicechat-development.md](docs/voicechat-development.md). Dynamic, Transfer-aware routing is implemented but remains experimental and requires real-client validation before it is considered production-ready.

For release gating, manual smoke checks, and non-blocking post-MVP work, see [docs/v0.1.0-readiness.md](docs/v0.1.0-readiness.md).

## Docker

```powershell
docker build -t mc-gateway:dev .
docker run --rm -p 25565:25565 -p 19132:19132/udp -p 127.0.0.1:9090:9090 -v ${PWD}/examples/config.yaml:/etc/mc-gateway/config.yaml:ro mc-gateway:dev
```

The Dockerfile uses a multi-stage build and `gcr.io/distroless/static-debian12:nonroot` for the runtime image. Distroless keeps the image small and removes shell/package-manager attack surface while retaining a minimal base with non-root support. Alpine is easier to debug interactively, but the runtime container should not require a shell for the MVP. If operational debugging becomes painful, a separate debug image target can be added later.

## Standalone binaries

Version tags publish `mc-gateway` archives for Linux, macOS, and Windows to the corresponding GitHub Release. Each release includes `checksums.txt`. Container images continue to be published separately by the Docker workflow.

Health checks are intentionally left to Kubernetes TCP probes in the MVP. A richer health endpoint can be added after a metrics or admin listener exists.

## Kubernetes

Apply the minimal example after replacing the image and backend service names:

```powershell
kubectl apply -f deploy/kubernetes/mc-gateway.yaml
```

The manifest includes:

- `Namespace`
- ConfigMap backed YAML config
- Deployment with non-root security context
- LoadBalancer Service on TCP `25565`
- Optional Bedrock forwarding requires exposing UDP `19132` when `bedrock.enabled` is true.
- TCP readiness and liveness probes

Namespace-scoped Kubernetes Service annotation discovery is implemented. If discovery is enabled, use the namespace-scoped RBAC example in `deploy/kubernetes/discovery-rbac.yaml`. See [docs/kubernetes-discovery.md](docs/kubernetes-discovery.md).

## Security Notes

- The handshake parser caps packet length.
- Server address length is limited.
- A read deadline is applied while waiting for the initial handshake.
- Backend dial timeout is configured.
- Unknown hosts are denied unless explicitly configured to use the default route.
- Logs include route-level metadata, not raw packet payloads.
- UDP relay payloads are opaque; relay sessions are bounded transport endpoint mappings, not player identity.
- Parser and network proxy are separate packages to keep tests focused.

See [docs/security.md](docs/security.md) for more detail.

## Contributing

Use a feature branch for every change and open a pull request against `main`.
See [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/development.md](docs/development.md) for the workflow, pre-PR checks, and GitHub branch protection recommendations.
