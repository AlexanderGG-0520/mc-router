# Kubernetes Discovery

## Purpose

Kubernetes discovery is planned as a way to generate gateway routes from Kubernetes resources instead of maintaining every Minecraft backend route by hand in the static YAML file.

The current implementation is groundwork only. It adds configuration types, validation, and a pure Service annotation parser. It does not connect to the Kubernetes API, watch resources, build route snapshots, or change runtime routing behavior.

## Why Service Annotation Discovery First

The first discovery mode is planned around Service annotations because Services are already the stable routing boundary for Minecraft backends. A Service has a stable DNS name, a namespace, and declared ports, which lets the gateway derive a backend without trusting arbitrary backend strings from annotations.

This also keeps the initial model namespace-scoped and easy to review:

- Operators opt in per Service.
- The generated backend uses Kubernetes Service DNS.
- The annotation port must match an existing Service port.
- No CRD installation is required for early testing.

## Why Other Modes Are Deferred

Pod annotations are deferred because Pods are not the stable routing contract and can change frequently during rollout, restart, or scale events.

EndpointSlice discovery is deferred because it moves the gateway closer to endpoint-level load balancing and readiness behavior. The gateway should rely on normal Kubernetes Service routing first unless endpoint awareness becomes necessary.

CRDs are deferred because the static config and Service annotation model should prove the route semantics before the project adds an API surface, manifests, conversion concerns, and controller behavior.

## Config

The planned discovery config shape is:

```yaml
discovery:
  kubernetes:
    enabled: false
    namespace: ""
    mode: "service-annotations"
    annotationPrefix: "mc-router.alexandergg.com"
```

Defaults:

- `enabled` defaults to `false`.
- `namespace` defaults to empty, which is reserved to mean the current namespace.
- `mode` currently only accepts `service-annotations`.
- `annotationPrefix` defaults to `mc-router.alexandergg.com`.

`annotationPrefix` must not be empty and must not include a slash. All-namespaces discovery is not implemented.

Reload behavior for discovery config will be defined in a later snapshot integration PR. Today, changing these values has no runtime effect because Kubernetes watches are not implemented.

## Annotation Example

```yaml
apiVersion: v1
kind: Service
metadata:
  name: smp
  namespace: minecraft
  annotations:
    mc-router.alexandergg.com/enabled: "true"
    mc-router.alexandergg.com/host: "smp.example.com"
    mc-router.alexandergg.com/port: "25565"
spec:
  ports:
    - name: minecraft
      port: 25565
      targetPort: 25565
```

Only `enabled: "true"` enables a route. Missing `enabled`, `enabled: "false"`, or any other value does not produce a route.

## Generated Backend DNS

For the Service above, the discovered route concept is:

```text
host: smp.example.com
backend: smp.minecraft.svc.cluster.local:25565
```

The backend host is generated from Service name and namespace. The backend port is the annotated port after validating that the same number exists in the Service ports list. Annotations do not allow arbitrary backend hostnames or `host:port` strings.

## Static Config Priority

Future snapshot integration should merge route sources with this order:

1. Static routes win over discovered routes.
2. Valid discovered routes are used next.
3. `defaultRoute` is evaluated last according to `unknownHostPolicy`.

Static route overlap is not handled by the current parser. It should be handled by the future merge builder with static routes taking priority.

## Duplicate Host Policy

Duplicate discovered hosts are unsafe because a controller cannot reliably infer the operator's intended backend. If the same host is discovered more than once, every discovered route for that host should be disabled and reported with a duplicate reason. The project should avoid first-wins behavior for discovered routes.

## Failure Policy Plan

If Kubernetes API list or watch fails after a good snapshot exists, the gateway is expected to keep the last known good discovered snapshot.

The startup policy for `discovery.kubernetes.enabled: true` when the initial list fails is not finalized. Failing startup is the safer default because it avoids silently serving an incomplete route set.

When a discovered Service route is removed or no longer valid, the discovered route is expected to disappear from the active route snapshot after the future watch/snapshot integration observes the change.

## RBAC Plan

RBAC is not included yet because the current implementation does not call the Kubernetes API.

A future Service annotation controller should start with namespace-scoped permissions for Services, and only add EndpointSlice, Pod, or cluster-wide permissions if a later implementation actually needs them.

## Metrics Plan

Metrics are not implemented yet. Candidate metrics:

```text
mc_gateway_kubernetes_discovery_events_total{result,reason}
mc_gateway_kubernetes_discovered_routes
mc_gateway_kubernetes_resource_errors_total{reason}
```

Discovery metrics should stay low-cardinality. Do not use namespace, Service name, or host as metric labels. Keep labels to bounded values such as `result` and `reason`.

## Security Notes

Anyone who can write Service annotations in a watched namespace can change routing for the gateway.

All-namespaces watch is deferred to reduce the blast radius of early discovery behavior.

Host validation is mandatory. Annotation values must not be trusted as already safe.

The backend must be generated from Service DNS plus a validated Service port. Do not accept arbitrary backend strings from annotations.

Future controller logs should report bounded skip reasons and counts. Do not dump raw Kubernetes objects, full annotation maps, or untrusted annotation values into logs.

If fallback responses are enabled on a public gateway, keep messages generic. Public status or login fallback can reveal information to scanners and bots.

## Implementation Slicing

This PR intentionally stops at:

- Discovery config types and validation.
- Kubernetes Service annotation parser tests.
- Duplicate discovered host helper tests.
- Documentation of the intended merge and operation policy.

Not implemented yet:

- Kubernetes client-go dependency.
- Kubernetes API initial list.
- Service watch controller.
- EndpointSlice watch controller.
- Snapshot integration.
- RBAC manifests.
- CRDs.
- Wake-up or scale-to-zero controller behavior.
- REST API or Web UI.

## Current Status

Current implementation is config and parser groundwork only. It does not watch Kubernetes yet and does not alter the gateway runtime route snapshot.
