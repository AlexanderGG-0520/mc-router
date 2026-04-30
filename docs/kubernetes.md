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

## ConfigMap Updates And Reload

The gateway supports `SIGHUP` config reload on Linux. In Kubernetes, this gives operators a choice after updating the ConfigMap-backed route config:

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
