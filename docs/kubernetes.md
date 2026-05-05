# Kubernetes

## Assumptions

- Minecraft workloads run on Kubernetes.
- Each Minecraft server or server group can live in its own namespace.
- Cilium provides CNI networking.
- CRI-O is the container runtime.
- The gateway has one public TCP entry point on `25565`.
- Backend targets are Kubernetes Services.

## Recommended Namespace Layout

Example:

```text
mc-gateway
alec-smp
alec-smp-lobby
```

Backends should be addressed by stable Service DNS names:

```text
alec-smp.alec-smp.svc.cluster.local:25565
alec-smp-lobby.alec-smp-lobby.svc.cluster.local:25565
```

## Minimal Deployment

The MVP manifest is in:

```text
deploy/kubernetes/mc-gateway.yaml
```

It creates:

- Namespace
- ConfigMap
- Deployment
- LoadBalancer Service
- TCP readiness probe
- TCP liveness probe
- Non-root security context

## RBAC

RBAC manifests are not included yet. If `discovery.kubernetes.enabled` is true, startup performs a Kubernetes Service initial list and the gateway ServiceAccount requires the following permissions in the watched namespace:

- `get`, `list` on `services`

Future watch-based discovery will also require:

- `watch` on `services`
- `get`, `list`, `watch` on `endpointslices` (if endpoint awareness is added)

Kubernetes Service annotation discovery is startup-only today. It does not watch for later Service changes. See [Kubernetes Discovery](kubernetes-discovery.md) for the config, annotation format, duplicate host policy, startup failure policy, and future controller scope.

If `discovery.kubernetes.namespace` is empty, startup resolves the current namespace from the Pod's ServiceAccount namespace file at `/var/run/secrets/kubernetes.io/serviceaccount/namespace`. This file read is not Kubernetes API RBAC; it comes from the mounted ServiceAccount volume. The gateway does not read the ServiceAccount token for namespace resolution.

When discovery is enabled, failure to resolve the namespace, create in-cluster Kubernetes config, or list Services fails startup. Keep `discovery.kubernetes.enabled: false` for static-only operation outside a Kubernetes Pod.

## Fallback Responses

Fallback responses are disabled by default. If you want denied status pings to return a generic Minecraft server list response instead of a TCP close, enable status fallback in the ConfigMap:

```yaml
fallback:
  enabled: true
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

If you also want denied login starts to return a Minecraft login-state disconnect packet instead of a TCP close, enable login fallback explicitly:

```yaml
fallback:
  enabled: true
  login:
    enabled: true
    respondOnRouteDenied: true
    message: "Server unavailable. Please try again later."
```

To also return the same generic status response when a known route or default route is selected but the backend Service is unavailable or the dial times out, opt in explicitly:

```yaml
fallback:
  enabled: true
  status:
    enabled: true
    respondOnBackendFailure: true
    motd: "Server unavailable"
```

Use generic MOTD and login disconnect messages. Unknown host fallback can reveal that a gateway is present, and backend failure fallback can reveal that a route exists but is unavailable. Do not include namespace names, backend service names, internal domains, readiness details, or operational runbook text. These fallbacks are Minecraft protocol responses only; they are not a substitute for Kubernetes readiness, Service health, or alerting. Backend failure login fallback is not implemented yet.

## Prometheus Scraping

Metrics are disabled by default. To scrape gateway metrics inside a cluster, enable the metrics listener in the ConfigMap:

```yaml
metrics:
  enabled: true
  listen: ":9090"
  path: "/metrics"
```

Expose the metrics listener only on an internal Service port. Do not publish it through a public LoadBalancer or internet-facing ingress; the endpoint is unauthenticated HTTP.

For fallback behavior, watch:

```promql
sum by (state, reason) (rate(mc_gateway_fallback_responses_total[5m]))
```

Expected `state` values are `status` and `login`. Expected `reason` values are `route_denied`, `backend_dial_failed`, and `backend_dial_timeout`. These labels are intentionally low-cardinality and do not include client IPs, requested hostnames, backend Services, namespaces, MOTD text, disconnect messages, or usernames. Use the fallback metric together with `mc_gateway_route_decisions_total` and `mc_gateway_backend_dials_total`; route-denied fallback still records a denied route decision, while backend failure status fallback keeps the route decision as matched or default and records the backend dial failure separately.

Example Service shape:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: mc-gateway
  namespace: mc-gateway
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "9090"
    prometheus.io/path: "/metrics"
spec:
  selector:
    app.kubernetes.io/name: mc-gateway
  ports:
    - name: minecraft
      port: 25565
      targetPort: 25565
    - name: metrics
      port: 9090
      targetPort: 9090
```

If your cluster uses the Prometheus Operator, add a `ServiceMonitor` or `PodMonitor` later according to that installation's conventions. This repository does not ship those CRDs in the MVP manifest.

## ConfigMap Updates And Reload

The gateway supports `SIGHUP` config reload on supported Unix platforms, including Linux, macOS, BSD, and Solaris. In Kubernetes, this gives operators a choice after updating the ConfigMap-backed route config:

- Send `SIGHUP` to the running gateway process so new connections use the updated routes without restarting the Pod.
- Use a rolling restart when changing startup-only settings, such as the listener address, or when your platform makes signal delivery operationally awkward.

Reload is atomic from the gateway's point of view. If the updated config is invalid, the running route snapshot stays active. Active connections are not disconnected by reload; new connections use the new route snapshot after a successful reload.

Kubernetes discovery is not re-run during `SIGHUP` reload. If discovery was enabled at startup, the startup-discovered routes remain active and are re-merged with the reloaded static config. Changes to Service annotations or discovery config require a process restart until watch-based discovery is implemented.

Kubernetes ConfigMap projected volumes are updated asynchronously. Do not send `SIGHUP` until the mounted file has the expected content in the Pod. If you need deterministic rollout behavior across replicas, use a rolling restart instead of relying on manual signal timing.

Example command shape:

```powershell
kubectl exec -n mc-gateway deploy/mc-gateway -- kill -HUP 1
```

This assumes PID 1 inside the container is `mc-gateway` and that the container image includes a compatible `kill` command. Some restricted images or runtime policies may make `kubectl exec` or signal delivery unavailable. In those environments, prefer a rolling restart and verify logs for `reload_success` or `reload_failed`.

## NodePort Option

If a LoadBalancer is not available, change the Service type:

```yaml
spec:
  type: NodePort
```

Then set a fixed `nodePort` if your cluster policy requires it.

## Network Policy

NetworkPolicy is not included in the MVP manifest because cluster policy differs by environment. A future hardened example should:

- Allow inbound TCP `25565` to gateway Pods.
- Allow gateway egress only to selected Minecraft Service ports.
- Deny unrelated namespace egress by default.

With Cilium, use standard Kubernetes NetworkPolicy first. Move to CiliumNetworkPolicy only for features that Kubernetes NetworkPolicy cannot express.

## Future Discovery

The first route discovery shape is startup-only Service annotation discovery. It is namespace-scoped and avoids requiring cluster-wide list/watch. See [Kubernetes Discovery](kubernetes-discovery.md).
