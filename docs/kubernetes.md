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
