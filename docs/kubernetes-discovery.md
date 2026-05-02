# Kubernetes Discovery

## Purpose

Kubernetes discovery is planned as a way to generate gateway routes from Kubernetes resources instead of maintaining every Minecraft backend route by hand in the static YAML file.

The current implementation is groundwork only. It adds configuration types, validation, a pure Service annotation parser, an in-memory controller core that builds a discovered route snapshot from `ServiceInput` values, a pure merge builder for static plus discovered routes, and a runtime route snapshot boundary that can accept discovered routes in memory. It does not connect to the Kubernetes API, watch resources, provide discovered routes at runtime, or change runtime routing behavior.

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

The merge builder applies this policy before a discovered route provider exists. If a discovered route normalizes to the same host as a static route, the static route is kept and the discovered route is ignored with `static_route_precedence`.

`defaultRoute` is not inserted into the explicit route list. The existing router should continue to evaluate it after explicit static and discovered routes according to `unknownHostPolicy`.

## Merge Builder Groundwork

The current merge builder is a pure in-memory builder:

```text
static routes + discovered routes + defaultRoute presence -> merged explicit routes
```

It normalizes route hosts, validates discovered route backends, ignores discovered routes that overlap static routes, and returns deterministic routes, ignored discovered routes, and summary counts. The discovered backend must remain a generated Kubernetes Service DNS backend in the form `service.namespace.svc.cluster.local:port`; arbitrary discovered backend hostnames are rejected by the merge builder.

Duplicate discovered hosts are expected to be removed by the controller core before merge. If duplicate discovered hosts reach the merge builder anyway, the merge builder ignores all discovered routes for that host with `duplicate_discovered_host`.

Merge ignored reasons are intentionally low-cardinality:

| Reason | Meaning |
| --- | --- |
| `static_route_precedence` | A discovered route normalized to a host already configured by a static route. |
| `duplicate_discovered_host` | More than one discovered route produced the same normalized host at merge time. |
| `invalid_discovered_route` | A discovered route had an invalid host, invalid backend, or non-Service-DNS backend. |

Unexpected merge-builder failures are not currently recovered into an ignored reason; they surface to the caller instead.

## Runtime Boundary Groundwork

Runtime route snapshot construction now has an internal boundary:

```text
validated config + discovered routes -> merged route snapshot + router
```

Startup and config reload both pass through this boundary, but the discovered route provider is still empty. That means the current runtime still behaves as static-config-only unless a future provider supplies discovered routes.

The ordering is intentional:

1. YAML is parsed.
2. Static config validation succeeds.
3. Static routes and discovered routes are merged.
4. The router is built from the merged explicit route list and the existing `defaultRoute`.

Invalid static config fails before the merge builder runs. The merge builder does not try to repair or reinterpret invalid static routes. Merge ignored routes and stats are retained on the route snapshot boundary for future logging and metrics, but discovery metrics are not implemented yet.

Kubernetes watches and controllers do not exist yet, so no goroutine, client, informer, or API connection feeds this boundary today.

## Duplicate Host Policy

Duplicate discovered hosts are unsafe because a controller cannot reliably infer the operator's intended backend. If the same host is discovered more than once, every discovered route for that host should be disabled and reported with a duplicate reason. The project should avoid first-wins behavior for discovered routes.

The controller core applies this policy to the discovered route set before returning a snapshot. Duplicate hosts are returned in deterministic order, and the affected resources are reported with `duplicate_host`.

## Controller Core Groundwork

The current controller core is a pure in-memory builder:

```text
[]ServiceInput -> discovered route snapshot
```

It uses the Service annotation parser, collects skipped resources by low-cardinality reason, disables duplicate discovered hosts, and returns routes in deterministic order. Invalid resources do not fail the whole snapshot; the builder returns the best valid discovered route set plus skip information.

This is an implementation step between parser/controller groundwork and a real Kubernetes watch controller. It still does not add `client-go`, Kubernetes API initial list, Kubernetes watch behavior, discovered route provider integration, RBAC manifests, or metrics.

Skip reasons are intentionally low-cardinality so future logs and metrics can aggregate them safely:

| Reason | Meaning |
| --- | --- |
| `disabled` | The Service did not opt in with `enabled: "true"`. |
| `invalid_annotation_prefix` | The configured annotation prefix is empty, non-canonical, or otherwise invalid. |
| `invalid_service_name` | The Service name cannot be used as a Kubernetes DNS label. |
| `invalid_namespace` | The namespace cannot be used as a Kubernetes DNS label. |
| `missing_host` | The enabled Service did not provide a non-empty host annotation. |
| `invalid_host` | The host annotation is not a valid normalized route host. |
| `missing_port` | The enabled Service did not provide a non-empty port annotation. |
| `invalid_port` | The port annotation is not an integer from 1 to 65535. |
| `port_not_found` | The annotated port is not present in the Service ports list. |
| `duplicate_host` | More than one discovered route produced the same normalized host, so all discovered routes for that host were disabled. |
| `unknown` | A defensive fallback for unexpected controller-core failures. |

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

The current implementation intentionally stops at:

- Discovery config types and validation.
- Kubernetes Service annotation parser tests.
- In-memory controller core that builds discovered route snapshots from `ServiceInput` values.
- In-memory merge builder that combines static and discovered explicit routes.
- Runtime route snapshot boundary that currently receives an empty discovered route set.
- Duplicate discovered host helper tests.
- Documentation of the intended merge and operation policy.

Not implemented yet:

- Kubernetes client-go dependency.
- Kubernetes API initial list.
- Service watch controller.
- EndpointSlice watch controller.
- Kubernetes discovered route provider integration.
- RBAC manifests.
- CRDs.
- Wake-up or scale-to-zero controller behavior.
- REST API or Web UI.

## Current Status

Current implementation is config, parser, in-memory controller core, merge-builder, and runtime merge-boundary groundwork only. It does not watch Kubernetes yet and does not provide discovered routes to the gateway runtime.
