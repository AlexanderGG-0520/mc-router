# mc-router

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
- Minecraft handshake parser with VarInt support.
- Requested `serverAddress` based static route matching.
- TCP proxying to selected backend `host:port`.
- Unknown host deny policy, with optional default route policy.
- Structured JSON logging through Go `log/slog`.
- Handshake read timeout and backend dial timeout.
- Graceful shutdown on SIGINT/SIGTERM.
- Unit tests for VarInt, handshake parsing, config loading, and route matching.
- Dockerfile and minimal Kubernetes manifest.
- GitHub Actions CI for `gofmt`, `go test`, `go vet`, and Docker build smoke.

Deferred by design:

- Kubernetes auto-discovery from labels, annotations, or CRDs.
- Scale-to-zero wake-up and scale-down control.
- Fallback server behavior.
- Simple Voice Chat or extra UDP/TCP port routing.
- Prometheus metrics.
- REST API.
- Web UI.
- CRD definitions and controllers.

## Config

Static YAML is the first supported route source:

```yaml
listen: ":25565"
handshakeTimeout: "5s"
backendDialTimeout: "5s"
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

## Docker

```powershell
docker build -t mc-gateway:dev .
docker run --rm -p 25565:25565 -v ${PWD}/examples/config.yaml:/etc/mc-gateway/config.yaml:ro mc-gateway:dev
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

RBAC is not included because the MVP does not watch Kubernetes resources.

## Security Notes

- The handshake parser caps packet length.
- Server address length is limited.
- A read deadline is applied while waiting for the initial handshake.
- Backend dial timeout is configured.
- Unknown hosts are denied unless explicitly configured to use the default route.
- Logs include route-level metadata, not raw packet payloads.
- Parser and network proxy are separate packages to keep tests focused.

See [docs/security.md](docs/security.md) for more detail.
