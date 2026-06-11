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
- TCP proxying to selected backend `host:port`.
- Unknown host deny policy, with optional default route policy.
- Optional fallback responses for denied status pings, backend status dial failures, and denied login starts.
- Structured JSON logging through Go `log/slog`.
- Prometheus metrics endpoint when explicitly enabled, including low-cardinality fallback response counters.
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
- Simple Voice Chat or extra UDP/TCP port routing.
- REST API.
- Web UI.
- CRD definitions and controllers.

## Config

Static YAML is the first supported route source:

```yaml
listen: ":25565"
handshakeTimeout: "5s"
backendDialTimeout: "5s"
metrics:
  enabled: true
  listen: ":9090"
  path: "/metrics"
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
unknownHostPolicy: "deny"
defaultRoute:
  backend: "lobby.default.svc.cluster.local:25565"
  mode: "allow"
routes:
  - serverAddress: "smp.example.com"
    backend: "alec-smp.alec-smp.svc.cluster.local:25565"
  - serverAddress: "lobby.example.com"
    backend: "alec-smp-lobby.alec-smp-lobby.svc.cluster.local:25565"
```

`unknownHostPolicy` supports:

- `deny`: close connections for hosts that do not match an explicit route.
- `default`: send unknown hosts to `defaultRoute.backend`.

Metrics are disabled by default. Set `metrics.enabled: true` to serve unauthenticated Prometheus text metrics on `metrics.listen` and `metrics.path`. Do not expose this HTTP listener directly to the public internet; it is intended for internal scraping, such as from a Kubernetes cluster Prometheus.

Fallback responses are counted with `mc_gateway_fallback_responses_total{state,reason}` after a fallback response packet is successfully written. Labels are intentionally bounded: `state` is `status` or `login`, and `reason` is one of the documented low-cardinality lifecycle reasons.

Fallback responses are disabled by default. Set `fallback.enabled: true` and `fallback.status.enabled: true` to answer selected status pings with a minimal Minecraft status response. Route denied status responses default to enabled once status fallback is enabled; backend failure status responses require `fallback.status.respondOnBackendFailure: true` because they can reveal that a configured route exists. Set `fallback.login.enabled: true` to return a protocol 767 login-state disconnect packet for denied login starts.

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

Local research and E2E setup for future Simple Voice Chat support is documented in [docs/voicechat-development.md](docs/voicechat-development.md). Simple Voice Chat remains deferred and is not supported yet.

For release gating, manual smoke checks, and non-blocking post-MVP work, see [docs/v0.1.0-readiness.md](docs/v0.1.0-readiness.md).

## Docker

```powershell
docker build -t mc-gateway:dev .
docker run --rm -p 25565:25565 -p 127.0.0.1:9090:9090 -v ${PWD}/examples/config.yaml:/etc/mc-gateway/config.yaml:ro mc-gateway:dev
```

The Dockerfile uses a multi-stage build and `gcr.io/distroless/static-debian12:nonroot` for the runtime image. Distroless keeps the image small and removes shell/package-manager attack surface while retaining a minimal base with non-root support. Alpine is easier to debug interactively, but the runtime container should not require a shell for the MVP. If operational debugging becomes painful, a separate debug image target can be added later.

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
- TCP readiness and liveness probes

Namespace-scoped Kubernetes Service annotation discovery is implemented. If discovery is enabled, use the namespace-scoped RBAC example in `deploy/kubernetes/discovery-rbac.yaml`. See [docs/kubernetes-discovery.md](docs/kubernetes-discovery.md).

## Security Notes

- The handshake parser caps packet length.
- Server address length is limited.
- A read deadline is applied while waiting for the initial handshake.
- Backend dial timeout is configured.
- Unknown hosts are denied unless explicitly configured to use the default route.
- Logs include route-level metadata, not raw packet payloads.
- Parser and network proxy are separate packages to keep tests focused.

See [docs/security.md](docs/security.md) for more detail.

## Contributing

Use a feature branch for every change and open a pull request against `main`.
See [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/development.md](docs/development.md) for the workflow, pre-PR checks, and GitHub branch protection recommendations.
