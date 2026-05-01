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

RBAC is omitted because the MVP does not call the Kubernetes API.

## Status Fallback

Fallback status responses are disabled by default. If you want denied status pings to return a generic Minecraft server list response instead of a TCP close, enable it in the ConfigMap:

```yaml
fallback:
  enabled: true
  status:
    enabled: true
    motd: "Server unavailable"
    protocolName: "mc-gateway"
    protocolVersion: 767
    maxPlayers: 0
    onlinePlayers: 0
```

Use a generic MOTD. Unknown host fallback can reveal that a gateway is present, so do not include namespace names, backend service names, internal domains, or operational details. Login disconnect fallback and backend failure fallback are not implemented yet.

## Prometheus Scraping

Metrics are disabled by default. To scrape gateway metrics inside a cluster, enable the metrics listener in the ConfigMap:

```yaml
metrics:
  enabled: true
  listen: ":9090"
  path: "/metrics"
```

Expose the metrics listener only on an internal Service port. Do not publish it through a public LoadBalancer or internet-facing ingress; the endpoint is unauthenticated HTTP.

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

Possible route definitions:

- Service annotations:
  - `mc-router.example.com/server-address=smp.example.com`
  - `mc-router.example.com/backend-port=25565`
- Namespace labels to opt into discovery.
- CRD for explicit cross-namespace routing.

Discovery should be namespace-scoped by default and should avoid requiring cluster-wide list/watch until there is a clear operational need.
