# Kubernetes Discovery

## Purpose

Kubernetes discovery is planned as a way to generate gateway routes from Kubernetes resources instead of maintaining every Minecraft backend route by hand in the static YAML file.

The current implementation can perform a startup-only Kubernetes Service initial list when `discovery.kubernetes.enabled` is true. It adds configuration types, validation, namespace resolution, a `client-go` Service lister, a pure Service annotation parser, an in-memory controller core that builds a discovered route snapshot from `ServiceInput` values, a pure merge builder for static plus discovered routes, and a runtime route snapshot boundary. It does not watch resources, start a background controller, or automatically update routes after startup.

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
- `namespace` defaults to empty, which means the gateway should resolve the current namespace from its in-cluster ServiceAccount namespace file.
- `mode` currently only accepts `service-annotations`.
- `annotationPrefix` defaults to `mc-router.alexandergg.com`.

`annotationPrefix` must not be empty and must not include a slash. All-namespaces discovery is not implemented.

Kubernetes discovery settings are startup settings in the current implementation. `SIGHUP` reload does not re-run the Kubernetes initial list.

## Namespace Resolution

When `discovery.kubernetes.namespace` is set to a non-empty value, that explicit namespace is used and no namespace file is read.

When `discovery.kubernetes.namespace` is empty, the gateway treats it as current-namespace discovery. The current namespace is resolved by reading only this Kubernetes ServiceAccount namespace file:

```text
/var/run/secrets/kubernetes.io/serviceaccount/namespace
```

The file content is trimmed for spaces and newlines, then validated as a Kubernetes DNS label. Missing files, unreadable files, empty files, or invalid namespace values are errors. With `discovery.kubernetes.enabled: true`, namespace resolution failures fail startup rather than silently listing the wrong scope.

The empty namespace value is not treated as all-namespaces discovery, and `metav1.NamespaceAll` is not used. Cluster-wide discovery remains unsupported.

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

Startup and config reload both pass through this boundary. At startup, `discovery.kubernetes.enabled: true` feeds discovered routes from one Kubernetes Service initial list into this boundary. When discovery is disabled, the provider is empty and the runtime remains static-config-only.

The ordering is intentional:

1. YAML is parsed.
2. Static config validation succeeds.
3. Static routes and discovered routes are merged.
4. The router is built from the merged explicit route list and the existing `defaultRoute`.

Invalid static config fails before the merge builder runs. The merge builder does not try to repair or reinterpret invalid static routes. Merge ignored routes and stats are retained on the route snapshot boundary for future logging and metrics, but discovery metrics are not implemented yet.

Kubernetes watches and controllers do not exist yet, so no goroutine, informer, or background API connection feeds this boundary after startup.

## Startup Initial List

When `discovery.kubernetes.enabled` is `false`, startup does not create an in-cluster Kubernetes client config, read the ServiceAccount namespace file, or call the Kubernetes API. Existing static-only behavior is preserved for non-Kubernetes environments.

When `discovery.kubernetes.enabled` is `true`, startup performs this sequence:

1. Parse and validate the static YAML config.
2. Resolve the namespace from `discovery.kubernetes.namespace` or the current namespace file.
3. Create an in-cluster Kubernetes client config with `rest.InClusterConfig`.
4. List Services once in the resolved namespace.
5. Convert Services to `ServiceInput`, skipping `ExternalName` Services.
6. Build discovered routes from Service annotations.
7. Merge static and discovered routes into the startup route snapshot.

Invalid Service annotations are skipped and reported in the in-memory discovery result. Duplicate discovered hosts are also skipped. Neither condition fails startup as long as the Kubernetes API list itself succeeded.

## Reload Limitation

`SIGHUP` reload does not re-run the Kubernetes API initial list. The discovered routes captured at startup remain active and are re-merged with the newly loaded static config. This avoids accidentally dropping discovered routes on reload, but it also means changes to Service annotations, Kubernetes Services, or discovery config values require a process restart until watch/snapshot integration is added.

## Duplicate Host Policy

Duplicate discovered hosts are unsafe because a controller cannot reliably infer the operator's intended backend. If the same host is discovered more than once, every discovered route for that host should be disabled and reported with a duplicate reason. The project should avoid first-wins behavior for discovered routes.

The controller core applies this policy to the discovered route set before returning a snapshot. Duplicate hosts are returned in deterministic order, and the affected resources are reported with `duplicate_host`.

## Controller Core Groundwork

The current controller core is a pure in-memory builder:

```text
[]ServiceInput -> discovered route snapshot
```

It uses the Service annotation parser, collects skipped resources by low-cardinality reason, disables duplicate discovered hosts, and returns routes in deterministic order. Invalid resources do not fail the whole snapshot; the builder returns the best valid discovered route set plus skip information.

This is an implementation step between parser/controller groundwork and a real Kubernetes watch controller. The controller core stays separate from the later `client-go` initial list helper, Kubernetes watch behavior, runtime provider integration, RBAC manifests, and metrics.

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

## Failure Policy

When `discovery.kubernetes.enabled` is `true`, startup fails for:

- namespace resolution failure
- in-cluster config creation failure
- initial Service list API failure
- invalid static config

Failing startup is intentional because silently falling back to static-only routing can hide missing discovered routes.

If Kubernetes API list or watch fails after a good snapshot exists, the gateway is expected to keep the last known good discovered snapshot. That behavior belongs to the future watch/snapshot integration. When a discovered Service route is removed or no longer valid, the discovered route is expected to disappear from the active route snapshot only after that future integration observes the change.

## RBAC

The repository includes a namespace-scoped RBAC example for startup initial list discovery:

```text
deploy/kubernetes/discovery-rbac.yaml
```

With startup initial list enabled, the ServiceAccount used by the gateway needs only these namespace-scoped permissions:

- `get`, `list` on `services`

Future watch-based discovery will also need `watch` on `services`. EndpointSlice, Pod, or cluster-wide permissions should be added only if a later implementation actually needs them.

All-namespaces discovery is not implemented. Supporting it later would require an explicit ClusterRole/ClusterRoleBinding design and separate review.

Reading the ServiceAccount namespace file is not a Kubernetes API call and is not controlled by Kubernetes RBAC. It depends on the Pod's projected ServiceAccount volume. The gateway does not need permissions for Secrets and must not read the ServiceAccount token for namespace resolution.

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

Namespace resolution reads only the ServiceAccount namespace file. It must not read the ServiceAccount token or CA certificate files.

Future logs should include bounded failure context for namespace resolution, but should avoid dumping raw namespace file content. Namespace names should not be used as metrics labels because they are high-cardinality operational data.

Host validation is mandatory. Annotation values must not be trusted as already safe.

The backend must be generated from Service DNS plus a validated Service port. Do not accept arbitrary backend strings from annotations.

Future controller logs should report bounded skip reasons and counts. Do not dump raw Kubernetes objects, full annotation maps, or untrusted annotation values into logs.

If fallback responses are enabled on a public gateway, keep messages generic. Public status or login fallback can reveal information to scanners and bots.

## Discovered Route Provider Interface

The project now includes an internal provider interface to bridge the controller core and the runtime route snapshot boundary:

```go
type RouteProvider interface {
    Routes(ctx context.Context) ([]kubernetes.DiscoveredRoute, error)
}
```

This interface allows the gateway runtime to fetch a snapshot of discovered routes without knowing the details of the underlying discovery source (e.g., Kubernetes API, memory).

### Memory Provider

A `MemoryProvider` is implemented for testing and early development. It stores a list of discovered routes in memory and returns a safe copy when requested.

### Kubernetes Service Initial List Groundwork

The project now includes `client-go` dependency and initial list groundwork for Kubernetes Services.

- `ServiceLister` interface: abstracts listing Services and converting them to `ServiceInput`.
- `ClientServiceLister`: implementation using `client-go`.
- `ToServiceInput`: pure conversion from `corev1.Service` to `ServiceInput`.

This implementation fetches a one-time snapshot of Services from the Kubernetes API at startup when Kubernetes discovery is enabled. It prepares Services for the controller core and feeds discovered routes into the startup route snapshot. It does not start a background watch loop or use informers.

### Current Namespace Helper

The project includes a helper for resolving the namespace that is passed to `ServiceLister.ListServices(ctx, namespace)`. It preserves the explicit namespace path and only reads the ServiceAccount namespace file when the configured namespace is empty.

### ExternalName Service Policy

Initial implementation skips `ExternalName` Services. The gateway discovery model relies on generating backends in the form `service.namespace.svc.cluster.local:port`. `ExternalName` Services point to arbitrary external DNS names, which breaks this assumption and can lead to untrusted or non-canonical backend addresses being generated.

### Snapshot Rebuild Helper

An internal helper function `RebuildRouteSnapshot` is provided to combine a validated static configuration with routes from a `RouteProvider`:

```go
func RebuildRouteSnapshot(ctx context.Context, cfg config.Config, provider discovery.RouteProvider) (RouteSnapshot, error)
```

If the provider is `nil`, it behaves as if discovery is disabled. If the provider returns an error, the rebuild fails, preventing a partial or stale route update from being applied to the runtime.

## Implementation Slicing

The current implementation intentionally stops at:

- Discovery config types and validation.
- Kubernetes Service annotation parser tests.
- In-memory controller core that builds discovered route snapshots from `ServiceInput` values.
- In-memory merge builder that combines static and discovered explicit routes.
- Internal `RouteProvider` interface and `MemoryProvider` implementation.
- `client-go` dependency added.
- Startup-only `ServiceLister` Kubernetes API initial list.
- `ToServiceInput` conversion from Kubernetes `corev1.Service`.
- Current namespace resolution helper for `discovery.kubernetes.namespace == ""`.
- `RebuildRouteSnapshot` helper for unified static/discovered route management.
- Runtime route snapshot boundary that receives startup-discovered routes when Kubernetes discovery is enabled.
- Duplicate discovered host helper tests.
- Documentation of the intended merge and operation policy.

Not implemented yet:

- Kubernetes API watch / informer loop.
- Background goroutine watch controller.
- Runtime auto-update after startup.
- Periodic resync.
- ClusterRole or all-namespaces RBAC manifests.
- CRDs.
- Wake-up or scale-to-zero controller behavior.
- REST API or Web UI.
- Discovery metrics.

## Current Status

Current implementation is config, parser, in-memory controller core, merge-builder, provider interface, current namespace helper, startup-only client-go initial list, and runtime merge-boundary integration. It does not watch Kubernetes yet and does not automatically update routes after startup.
