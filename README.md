# mc-router

![Docker Build](https://img.shields.io/github/actions/workflow/status/AlexanderGG-0520/mc-router/docker-publish.yml?branch=main)
[![Docker Pulls](https://img.shields.io/docker/pulls/alecjp02/mc-router.svg?logo=docker)](https://hub.docker.com/r/alecjp02/mc-router/)
[![Docker Stars](https://img.shields.io/docker/stars/alecjp02/mc-router.svg?logo=docker)](https://hub.docker.com/r/alecjp02/mc-router/)
[![GitHub Issues](https://img.shields.io/github/issues-raw/alexandergg-0520/mc-router.svg)](https://github.com/alexandergg-0520/mc-router/issues)
![GHCR](https://img.shields.io/badge/GHCR-ghcr.io%2Falexandergg--0520%2Fmc--router-blue)
![License](https://img.shields.io/badge/license-MIT-green)
![Kubernetes](https://img.shields.io/badge/kubernetes-ready-blue)

`mc-router` is a lightweight Minecraft gateway that lets multiple Minecraft servers share one public entry point.

For Java Edition, it reads the hostname from the initial Minecraft handshake, selects a backend, forwards the original handshake, and then proxies the TCP stream in both directions. The project is called `mc-router`; the executable is currently named `mc-gateway`.

```text
                               +--------------------+
hub.example.com  -----------+  |                    | ---> hub:25565
                            +->|     mc-router      |
smp.example.com  -----------+  |    public :25565   | ---> smp:25565
                               |                    |
                               +--------------------+
```

## Why mc-router exists

Running several Minecraft servers usually creates an awkward networking choice:

- expose a different public port for every server;
- put every server behind a larger gameplay proxy stack;
- or build routing logic into the Minecraft server containers themselves.

`mc-router` exists to keep that responsibility at the network edge instead.

A typical deployment has several DNS names pointing to the same public address:

```text
hub.example.com -> 203.0.113.10
smp.example.com -> 203.0.113.10
```

Players still connect to the normal Minecraft port. `mc-router` uses the hostname the client actually sent in the Minecraft handshake to decide which internal backend should receive the connection.

That gives the deployment one public listener while allowing each backend to remain an independent Minecraft workload.

This separation is deliberate. A Minecraft server image should be responsible for running one Minecraft server and managing its lifecycle. Public routing, connection policy, discovery, metrics, fallback behavior, and related edge concerns belong to a separate component so they can evolve without rebuilding every backend image.

## Why these technologies and boundaries

The implementation is deliberately optimized around a network-edge daemon rather than a gameplay proxy or a Minecraft server distribution.

### Go for the gateway process

Go fits the runtime shape of `mc-router`: a long-running network process with many concurrent TCP or UDP sessions, explicit cancellation, a small deployment footprint, and a need to ship the same program as a container or standalone binary.

The Java routing path can therefore remain mostly standard-library networking code instead of depending on a full Minecraft server or plugin runtime.

### Inspect only what is needed to route

For normal Java traffic, `mc-router` parses the initial handshake because that is where Minecraft exposes the requested server address and next state. After a route is selected, it forwards the original handshake bytes and treats the rest of the connection as an opaque TCP stream.

That boundary is important: the router should not become the owner of gameplay protocol semantics merely because it needed one protocol field to choose a destination.

It also means ordinary routing does not require Paper, Fabric, Velocity, or a backend plugin. Vanilla-compatible, Fabric, Paper, and other Java servers can all sit behind the same routing model as long as they accept the proxied TCP connection.

### Explicit YAML first, discovery second

Static YAML is the baseline route source because it is easy to review, version, reproduce, and validate without requiring an external control plane.

Kubernetes discovery is implemented as another route provider feeding the same route-snapshot model. Discovery does not replace the routing model with a Kubernetes controller-specific one; it supplies routes to it.

This keeps the core routing semantics testable outside Kubernetes while still allowing Kubernetes-native deployments to discover Services dynamically.

### Keep workload lifecycle outside routing correctness

`mc-router` can emit a scaler webhook event, but it does not require the router process to own a Docker daemon or directly scale a Kubernetes workload in order to decide where a connection belongs.

The intended boundary is:

```text
Minecraft connection -> route decision -> backend dial
                          |
                          +-> optional external notification/control integration
```

A failed optional control-plane notification should not silently redefine the selected Minecraft route.

The same principle applies to backend availability notifications: monitoring information can be exported to another component without becoming an implicit source of truth for LOGIN, Transfer, or ordinary STATUS routing.

### Add protocol-aware components only where opaque forwarding is insufficient

The project does use deeper protocol integration when the transport itself makes simple forwarding impossible.

For Bedrock `host-proxy`, the requested hostname appears inside the Bedrock login flow rather than in an initial opaque UDP datagram, so gophertunnel is used to terminate and re-originate that session.

Transfer-aware Simple Voice Chat has a different problem again: UDP voice packets do not identify the Minecraft backend that currently owns a player after a Transfer. That feature therefore uses small Fabric/Paper companions to register short-lived player UUID -> backend ownership with the router.

Those are explicit exceptions driven by protocol requirements, not a reason to make the ordinary Java path protocol-heavy.

### Small runtime container

The production container uses a distroless non-root runtime image. The gateway should not need a shell or package manager to proxy traffic, so the default image minimizes runtime surface instead of optimizing for interactive container debugging.

## Relationship to itzg/mc-router

This repository shares its name and the same broad starting problem with [itzg/mc-router](https://github.com/itzg/mc-router): route Minecraft Java connections by the hostname requested by the client so several servers can share one public IP address and the standard Minecraft port.

That similarity is intentional at the problem/name level, but this repository is an independent implementation, not a fork or a drop-in replacement for `itzg/mc-router`.

The projects differ mainly in what they choose to make the router process responsible for.

`itzg/mc-router` has evolved into a broad, mature routing service with first-class Docker, Docker Swarm, and Kubernetes discovery, direct Docker/Kubernetes auto-scaling, ngrok integration, connection webhooks, login-aware metrics, and other operational features around the core hostname router.

This repository instead treats the router primarily as a network-edge component and tries to preserve explicit boundaries between:

- route selection;
- Minecraft transport;
- backend lifecycle control;
- monitoring state;
- backend-owned player state;
- and protocol-specific auxiliary transports.

A simplified comparison:

| Design question | `itzg/mc-router` | This repository |
| --- | --- | --- |
| Core Java routing | Hostname from Minecraft handshake -> backend | Hostname from Minecraft handshake -> backend |
| Implementation lineage | Original `itzg/mc-router` project | Independent implementation; not a fork |
| Static routing | CLI/environment mappings and route config | YAML route model |
| Dynamic discovery | Docker, Docker Swarm, Kubernetes | Namespace-scoped Kubernetes Service discovery feeding route snapshots |
| Docker daemon integration | First-class discovery and lifecycle integration | Not required; containers are only one deployment method |
| Kubernetes workload scaling | Can directly scale supported workloads and coordinate wake/sleep behavior | Optional webhook integration leaves the workload controller external |
| Java protocol inspection | Can inspect Login Start for player-aware features such as login metrics and scale-up access policy | Normal path stops at the initial handshake needed for routing |
| STATUS during backend lifecycle events | Can generate asleep/loading responses as part of auto-scale behavior | Transparent by default; router-generated STATUS requires explicit `statusOverride` or fallback configuration |
| Monitoring relationship to routing | Operational lifecycle features are integrated into the router | Availability monitoring is intentionally prevented from implicitly controlling LOGIN/Transfer/ordinary STATUS |
| Additional protocol paths | Focused on its established Java routing and infrastructure integrations | Also experiments with Bedrock/Geyser routing and Transfer-aware Simple Voice Chat UDP routing |

Neither model is universally better.

If the desired experience is especially Docker-centric and benefits from the router directly discovering and starting/stopping containers, the upstream `itzg/mc-router` design can be a better fit.

If the desired architecture treats Minecraft backends as independent workloads and wants routing behavior to remain stable even as monitoring, lifecycle automation, Hub logic, and auxiliary transports evolve separately, this repository is designed around that boundary.

The difference is therefore not simply that one project has or lacks a feature. Similar operational problems led to different answers to the architectural question:

> What is the router allowed to own?

That answer explains why two Go programs solving the same hostname-routing problem can end up with substantially different configuration, implementation structure, failure semantics, and extension points.

## How Java routing works

The normal Java Edition path is intentionally small:

1. A client connects to `mc-router`, normally on TCP `25565`.
2. `mc-router` reads the first Minecraft handshake packet.
3. It extracts the requested `serverAddress` and the requested next state.
4. It normalizes the configured host identity and selects a route.
5. It connects to the selected backend.
6. It forwards the exact original handshake bytes.
7. It proxies the remaining TCP stream in both directions.

For ordinary Java LOGIN and STATUS traffic, `mc-router` does not generate or reinterpret the backend's Minecraft response.

STATUS has two explicit routing controls:

```text
statusOverride -> statusBackend -> backend
```

- `statusOverride` returns a configured router-generated STATUS response and does not contact a backend.
- `statusBackend` sends only Java STATUS traffic to another backend.
- without either setting, STATUS uses the normal `backend`.
- LOGIN and Minecraft Transfer traffic always use the normal `backend`.

Monitoring state and backend availability notifications are separate concerns. They do not implicitly change this routing order or decide what Minecraft STATUS response a player receives.

## Quick start

The smallest useful setup needs two things:

1. one or more Minecraft backends reachable from `mc-router`;
2. DNS names that players use to reach the public `mc-router` listener.

### 1. Create a config

Create `config.yaml`:

```yaml
listen: ":25565"
handshakeTimeout: "5s"
backendDialTimeout: "5s"

unknownHostPolicy: "deny"

routes:
  - serverAddress: "hub.example.com"
    backend: "hub:25565"

  - serverAddress: "smp.example.com"
    backend: "smp:25565"
```

The backend addresses above are examples. They can be Docker service names, Kubernetes Services, private IP addresses, or any other address reachable from the `mc-router` process.

### 2. Point DNS at the router

Both public hostnames can resolve to the same public IP:

```text
hub.example.com -> your mc-router public IP
smp.example.com -> your mc-router public IP
```

The DNS result gets the player to the router. The hostname in the Minecraft handshake tells `mc-router` which backend to use.

### 3. Run mc-router

Using the current stable container release as an example:

```bash
docker run --rm \
  -p 25565:25565 \
  -v "$PWD/config.yaml:/etc/mc-gateway/config.yaml:ro" \
  ghcr.io/alexandergg-0520/mc-router:0.9.1
```

For production, pin the image to the release version you have validated rather than tracking `main`.

At this point:

```text
hub.example.com:25565 -> hub:25565
smp.example.com:25565 -> smp:25565
```

No plugin is required on the Java backend for this basic routing path.

## Example use cases

The basic route table is intentionally small, but the same boundary supports several deployment patterns.

### Several independent servers behind one public port

A home lab, VPS, or Kubernetes cluster can expose only TCP `25565` publicly while routing different DNS names to different servers:

```text
vanilla.example.com  -> vanilla backend
fabric.example.com   -> Fabric backend
creative.example.com -> creative backend
```

The backends do not need to know about each other and do not need to expose their own public ports.

### Hub plus directly addressable game servers

A network can send its main hostname to a Hub while still allowing explicitly named game servers to be reached through the same edge:

```yaml
routes:
  - serverAddress: "play.example.com"
    backend: "hub:25565"

  - serverAddress: "smp.example.com"
    backend: "survival:25565"

  - serverAddress: "creative.example.com"
    backend: "creative:25565"
```

This is useful when the Hub is a navigation layer rather than the transport owner for every gameplay connection. Minecraft Transfer can reconnect a client through the same public router and select another backend by hostname.

### Keep server-list STATUS separate from joins

A backend used for gameplay does not have to be the component that answers server-list STATUS.

`statusBackend` can proxy STATUS to another Minecraft endpoint while LOGIN and Transfer still use the gameplay backend. `statusOverride` can instead provide an intentionally static router-owned STATUS.

This can be used for a Hub-facing server list, a dedicated status responder, or a deliberately static maintenance/presentation response without redefining the login route.

### Kubernetes edge routing without public Services for every Minecraft server

Only `mc-router` needs the public L4 entry point. Minecraft backends can remain ClusterIP Services in their own namespaces.

Static YAML works for fixed deployments, while namespace-scoped Service annotation discovery can update the same route model as Services appear or change.

### One public Bedrock port for several Geyser backends

When Geyser exists on multiple backends, Bedrock `host-proxy` can use one public UDP `19132` listener and route by the Bedrock login `ServerAddress` instead of allocating a separate public UDP port to each backend.

This does not replace Geyser; it routes Bedrock sessions to the appropriate Geyser-enabled target.

### Transfer-aware Simple Voice Chat across backends

For networks using Minecraft Transfer instead of a gameplay proxy owning the whole player session, the experimental voice-routing companions can keep one public Simple Voice Chat UDP endpoint while updating which backend currently owns each player's voice traffic.

This is a specialized feature and is still subject to the real-client validation described below.

### Export operational state without giving it routing authority

Prometheus metrics, backend availability notifications, and scaler webhook events can feed monitoring, a Hub UI, an external controller, or other automation.

Those integrations are useful when operators want observability and control-plane hooks while keeping the Minecraft route itself explicit and independently testable.

### When this is not the right layer

`mc-router` is not intended to replace a gameplay proxy when the desired feature fundamentally requires a shared proxy-owned Minecraft session, cross-server plugin messaging, proxy commands, or another gameplay-level abstraction.

Likewise, operators whose primary requirement is deep Docker daemon discovery and built-in container lifecycle management should evaluate `itzg/mc-router` directly rather than assuming this repository is a compatible reimplementation.

## Routing configuration

### Multiple hostnames for one backend

Use `aliases` when several explicit host identities should map to the same route:

```yaml
routes:
  - serverAddress: "play.example.com"
    aliases:
      - "hub.example.com"
      - "play.local"
    backend: "hub:25565"
```

Aliases are explicit handshake identities. `mc-router` does not infer aliases from DNS resolution or IP equivalence.

### Unknown hosts

By default, a public Minecraft listener can receive handshakes for hostnames you did not intend to serve. The recommended strict policy is:

```yaml
unknownHostPolicy: "deny"
```

If you intentionally want unmatched hosts to go to a default backend:

```yaml
unknownHostPolicy: "default"

defaultRoute:
  backend: "lobby:25565"
  mode: "allow"
```

### Send STATUS to another backend

`statusBackend` is useful when the server-list ping should be answered by a different Minecraft backend while actual joins still go to the normal backend:

```yaml
routes:
  - serverAddress: "play.example.com"
    backend: "smp:25565"
    statusBackend: "status-service:25565"
```

The STATUS connection is proxied as Minecraft TCP traffic. The selected status backend remains responsible for its MOTD, version information, player sample, favicon, and other STATUS fields.

### Return an explicit static STATUS

When a route intentionally needs a router-owned static STATUS response:

```yaml
routes:
  - serverAddress: "play.example.com"
    backend: "smp:25565"
    statusOverride:
      motd: "Alec SMP"
      protocolName: "Alec SMP 2"
      protocolVersion: 767
      maxPlayers: 100
      onlinePlayers: 0
```

`statusOverride` is an explicit exception to transparent STATUS proxying. It takes precedence over `statusBackend` and `backend`, but it does not affect LOGIN or Transfer routing.

### Explicit fallback responses

Fallback responses are also opt-in. They are intended for configured error cases, not as an implicit health-state controller.

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

Keep `respondOnBackendFailure` disabled unless you intentionally want backend dial failures to produce the configured router-generated STATUS response.

## Docker

The runtime image is built with a distroless non-root base. The container entrypoint is `/mc-gateway`, and the default config path is `/etc/mc-gateway/config.yaml`.

Build locally:

```bash
docker build -t mc-gateway:dev .
```

Run a local build:

```bash
docker run --rm \
  -p 25565:25565 \
  -p 19132:19132/udp \
  -p 127.0.0.1:9090:9090 \
  -v "$PWD/examples/config.yaml:/etc/mc-gateway/config.yaml:ro" \
  mc-gateway:dev
```

Published images are available from GHCR and, when configured by the release workflow, Docker Hub.

## Kubernetes

`mc-router` is designed to work cleanly as a separate edge workload in Kubernetes.

A common shape is:

```text
Internet
   |
LoadBalancer / tunnel / L4 entry point
   |
mc-router
   |
   +----> mc-hub.mc-hub.svc.cluster.local:25565
   |
   +----> survival.survival.svc.cluster.local:25565
   |
   +----> creative.creative.svc.cluster.local:25565
```

Apply the minimal example after replacing its image and backend names:

```bash
kubectl apply -f deploy/kubernetes/mc-gateway.yaml
```

The example includes a ConfigMap-backed YAML config, Deployment, non-root security context, LoadBalancer Service, and TCP readiness/liveness probes.

For dynamic namespace-scoped Service annotation discovery, see [docs/kubernetes-discovery.md](docs/kubernetes-discovery.md). For the broader Kubernetes deployment model, see [docs/kubernetes.md](docs/kubernetes.md).

## Bedrock Edition

Bedrock uses UDP and is handled separately from the Java TCP listener.

Two modes are available:

- `udp-forward`: opaque UDP forwarding to one configured backend.
- `host-proxy`: Bedrock-aware routing through gophertunnel using the client's requested `ServerAddress`.

Example:

```yaml
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

    - name: survival
      hosts:
        - "survival.play.example.com"
      backend: "mc-survival.mc-survival.svc.cluster.local:19132"
```

`mc-router` does not implement Bedrock-to-Java translation. A Geyser-enabled backend or separate Geyser service is still required.

`host-proxy` terminates and re-originates the Bedrock session and therefore has a larger compatibility surface than the Java TCP path or opaque `udp-forward` mode. Validate it against the exact Geyser/Floodgate setup used in production.

## Transfer-aware Simple Voice Chat routing

`mc-router` includes experimental dynamic UDP routing for [Simple Voice Chat](https://modrinth.com/plugin/simple-voice-chat).

The problem is different from ordinary TCP routing: after a Minecraft Transfer, the same player may now belong to another backend, while Simple Voice Chat still uses a shared public UDP endpoint.

The experimental design uses:

- `mc-gateway` as the shared UDP listener and authenticated registration API;
- one Fabric or Paper companion JAR on each participating backend;
- short-lived player UUID -> backend registrations refreshed while the player remains connected.

Replacing a registration closes stale UDP sessions so late packets from a previous backend are not forwarded after Transfer.

Unknown, expired, malformed, or ambiguous sessions fail closed. `mc-router` does not decrypt, inspect, or log voice payloads or Simple Voice Chat secrets.

This feature has unit and local transport coverage but still requires real-client validation for Transfer event ordering, reconnection behavior, sustained multi-client operation, restart behavior, and the intended production network path. A brief voice interruption during Transfer is expected.

Build companion JARs with:

```bash
gradle -p companion :fabric:jar :paper:jar
```

Generated artifacts:

```text
companion/fabric/build/libs/mc-router-voicechat-companion-fabric-<version>.jar
companion/paper/build/libs/mc-router-voicechat-companion-paper-<version>.jar
```

Install exactly one platform-appropriate companion on each participating backend. Simple Voice Chat itself must be installed separately.

Required environment variables:

| Variable | Required | Purpose |
| --- | --- | --- |
| `MC_ROUTER_VOICECHAT_REGISTRATION_URL` | Yes | Internal registration API base URL |
| `MC_ROUTER_VOICECHAT_BACKEND_ID` | Yes | Backend ID matching `voiceChat.backends` |
| `MC_ROUTER_VOICECHAT_TOKEN` | Yes | Authentication token for that backend |
| `MC_ROUTER_VOICECHAT_PUBLIC_HOST` | Yes | Public voice endpoint, including UDP port |
| `MC_ROUTER_VOICECHAT_INSTANCE_ID` | No | Stable server-instance owner ID |
| `MC_ROUTER_VOICECHAT_TTL` | No | Registration lease timing basis |
| `MC_ROUTER_VOICECHAT_REFRESH_INTERVAL` | No | Lease refresh interval |
| `MC_ROUTER_VOICECHAT_REQUEST_TIMEOUT` | No | Registration HTTP timeout |
| `MC_ROUTER_VOICECHAT_MAX_TRACKED_PLAYERS` | No | Maximum locally tracked players |

Keep the registration API and authentication tokens on a trusted internal network.

See [docs/voicechat-routing-design.md](docs/voicechat-routing-design.md) for the protocol design and threat model, and [docs/voicechat-development.md](docs/voicechat-development.md) for local development and E2E setup.

## Backend availability notifications

`mc-router` can independently probe explicitly configured Minecraft backends and notify the Hub control companion when their observed availability changes.

```yaml
availability:
  enabled: true
  interval: "10s"
  timeout: "3s"
  controlURL: "http://mc-router-control.mc-hub.svc.cluster.local:8082"
  tokenEnv: "MC_ROUTER_CONTROL_TOKEN"
  backends:
    - id: "alec-smp-2"
      address: "survival.survival.svc.cluster.local:25565"
      serverAddress: "smp.example.com"
```

The probe performs a Java STATUS handshake and request; a bare TCP connect is not considered sufficient evidence that the backend is online.

This monitoring path is intentionally separate from player routing. Availability state is notification data and does not implicitly redirect LOGIN, Transfer, or ordinary STATUS traffic.

`mc-router-control-companion-paper` is a separate Paper plugin intended for an internal Hub. It accepts authenticated backend availability updates and emits a `BackendAvailabilityChangeEvent` when state changes. Do not expose its control listener publicly.

## Other optional controls

The example configuration also documents optional features including:

- source IP/CIDR allow and deny policy;
- per-source-IP connection rate limiting;
- trusted PROXY Protocol peers;
- Prometheus metrics;
- config-file reload watching;
- a scaler webhook;
- fixed-backend UDP relay.

See [examples/config.yaml](examples/config.yaml) for the current configuration surface and [docs/architecture.md](docs/architecture.md) for the corresponding runtime behavior.

## Run from source

```bash
go run ./cmd/mc-gateway -config examples/config.yaml
```

For a local backend, point one route at a local server such as `127.0.0.1:25566`.

## Test

```bash
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
```

The normal test suite uses fake protocol backends and does not require a real Minecraft server.

A separate optional real-server E2E smoke test can be run locally with Docker or through the manual GitHub Actions workflow. See [docs/e2e.md](docs/e2e.md).

## Standalone binaries

Version tags publish `mc-gateway` archives for Linux, macOS, and Windows through GitHub Releases, together with `checksums.txt`.

Container images are published separately by the Docker workflow.

## Security model

The gateway is intentionally strict at the public edge:

- initial handshake packet length is capped;
- server-address length is limited;
- handshake reads have a deadline;
- backend dialing has a timeout;
- unknown hosts can be denied before any backend connection;
- logs contain route-level metadata rather than raw Minecraft payloads;
- UDP transport sessions are bounded;
- metrics and internal control APIs are intended for trusted networks, not direct public exposure.

See [docs/security.md](docs/security.md) for more detail.

## Documentation

- [Architecture](docs/architecture.md) - connection lifecycle and design boundaries.
- [Kubernetes](docs/kubernetes.md) - deployment model.
- [Kubernetes discovery](docs/kubernetes-discovery.md) - namespace-scoped Service annotation discovery.
- [E2E testing](docs/e2e.md) - real-server smoke validation.
- [Development](docs/development.md) - contributor workflow and local development.
- [Simple Voice Chat design](docs/voicechat-routing-design.md) - dynamic UDP routing design and threat model.
- [Simple Voice Chat development](docs/voicechat-development.md) - experimental companion development and validation.
- [Security](docs/security.md) - parser, routing, and network-security notes.

## Contributing

Use a feature branch for every change and open a pull request against `main`.

See [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/development.md](docs/development.md) for the development workflow, pre-PR checks, and repository conventions.