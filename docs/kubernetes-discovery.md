# Kubernetes Discovery

## Purpose

Kubernetes discovery generates gateway routes from Kubernetes Services instead of requiring every Minecraft backend route to be maintained by hand in the static YAML file.

The current implementation performs a Kubernetes Service initial list at startup when `discovery.kubernetes.enabled` is true, then starts a namespace-scoped Service watch controller under a retry/backoff supervisor. It adds configuration types, validation, namespace resolution, a `client-go` Service lister, a pure Service annotation parser, a pure controller core that builds a discovery `Result` from `ServiceInput` values, a Kubernetes `SnapshotProvider` that exposes route-only snapshots through `RouteProvider`, a namespace-scoped Service watch controller core, a pure merge builder for static plus discovered routes, runtime route snapshot updates, and watch failure recovery.

Watch updates rebuild the route snapshot from the latest valid static config plus the latest complete discovered route set. The active route snapshot is swapped only after rebuild succeeds.

## Why Service Annotation Discovery First

The first discovery mode uses Service annotations because Services are already the stable routing boundary for Minecraft backends. A Service has a stable DNS name, a namespace, and declared ports, which lets the gateway derive a backend without trusting arbitrary backend strings from annotations.

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

The discovery config shape is:

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

`annotationPrefix` must be a canonical lowercase DNS subdomain. It must not be empty, include leading or trailing whitespace, or include a slash. All-namespaces discovery is not implemented.

Kubernetes discovery settings are startup settings in the current implementation. `SIGHUP` reload does not re-resolve the discovery namespace or restart the watch controller.

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

## Annotation Contract

With the default `annotationPrefix`, Service annotation discovery reads exactly these annotations:

| Annotation | Required value |
| --- | --- |
| `mc-router.alexandergg.com/enabled` | Must be the literal string `"true"` to opt in. Missing values, `"false"`, different casing, or any other value disable discovery for that Service. |
| `mc-router.alexandergg.com/host` | Required after opt-in. Must be a non-empty valid Minecraft route host. The value is normalized before it becomes the discovered route host. |
| `mc-router.alexandergg.com/port` | Required after opt-in. Whitespace is trimmed, then the value must parse as an integer from `1` to `65535` and match a declared `spec.ports[].port` on the Service. |

If `discovery.kubernetes.annotationPrefix` is changed, the same suffixes are read under that prefix: `/enabled`, `/host`, and `/port`.

Discovery does not read an annotation for the backend hostname. The backend is generated as `service.namespace.svc.cluster.local:port` after the Service name, namespace, and annotated port pass validation. `ExternalName` Services are skipped before annotation parsing because they do not fit this generated Service-DNS backend contract.

Missing or invalid annotations skip only that Service. They do not fail startup and do not stop runtime watch processing as long as the Kubernetes API list or watch itself succeeds.

## Generated Backend DNS

For the Service above, the discovered route concept is:

```text
host: smp.example.com
backend: smp.minecraft.svc.cluster.local:25565
```

The backend host is generated from Service name and namespace. The backend port is the annotated port after validating that the same number exists in the Service ports list. Annotations do not allow arbitrary backend hostnames or `host:port` strings.

## Static Config Priority

Runtime snapshot integration merges route sources with this order:

1. Static routes win over discovered routes.
2. Valid discovered routes are used next.
3. `defaultRoute` is evaluated last according to `unknownHostPolicy`.

The merge builder applies this policy when static and discovered routes are combined. If a discovered route normalizes to the same host as a static route, the static route is kept and the discovered route is ignored with `static_route_precedence`.

`defaultRoute` is not inserted into the explicit route list. The existing router should continue to evaluate it after explicit static and discovered routes according to `unknownHostPolicy`.

## Merge Builder

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

## Runtime Boundary

Runtime route snapshot construction now has an internal boundary:

```text
validated config + discovered routes -> merged route snapshot + router
```

Startup and config reload both pass through this boundary. At startup, `discovery.kubernetes.enabled: true` builds one Kubernetes discovery `Result`, converts `Result.Routes` through `SnapshotProvider`, and feeds that route-only provider into this boundary. When discovery is disabled, the provider is empty and the runtime remains static-config-only.

The ordering is intentional:

1. YAML is parsed.
2. Static config validation succeeds.
3. Static routes and discovered routes are merged.
4. The router is built from the merged explicit route list and the existing `defaultRoute`.

Invalid static config fails before the merge builder runs. The merge builder does not try to repair or reinterpret invalid static routes. Merge ignored routes and stats are retained on the route snapshot boundary for logging and future resource-level metrics.

When Kubernetes discovery is enabled, a runtime goroutine starts the namespace-scoped Service watch controller supervisor after the startup snapshot is built. Successful watch updates feed this boundary and atomically replace the active route snapshot for new connections.

## Startup Initial List

When `discovery.kubernetes.enabled` is `false`, startup does not create an in-cluster Kubernetes client config, read the ServiceAccount namespace file, or call the Kubernetes API. Existing static-only behavior is preserved for non-Kubernetes environments.

When `discovery.kubernetes.enabled` is `true`, startup performs this sequence:

1. Parse and validate the static YAML config.
2. Resolve the namespace from `discovery.kubernetes.namespace` or the current namespace file.
3. Create an in-cluster Kubernetes client config with `rest.InClusterConfig`.
4. List Services once in the resolved namespace.
5. Convert Services to `ServiceInput`, skipping `ExternalName` Services.
6. Build a Kubernetes discovery `Result` from Service annotations.
7. Convert `Result.Routes` through `NewSnapshotProviderFromResult`.
8. Merge static and discovered routes into the startup route snapshot.

Invalid Service annotations are skipped and reported in the discovery `Result`. Duplicate discovered hosts are also skipped. Neither condition fails startup as long as the Kubernetes API list itself succeeded. Skipped Services, duplicate host metadata, and skipped reason counts stay on the `Result` for logging and runtime metrics; they are not exposed through `RouteProvider`.

## Runtime Watch Updates

The Service watch controller performs its own initial list, starts a direct `client-go` watch from the returned resource version, and sends a complete discovery `Result` to the runtime after each add/update/delete event. The low-level controller remains a one-shot watch core. A supervisor owns retry and backoff after runtime watch failures.

On each successful sink update, the runtime:

1. Converts the complete `Result` to a route-only `SnapshotProvider`.
2. Calls `UpdateDiscoveredRoutes` with that `RouteProvider`, which rebuilds a `RouteSnapshot` using the latest valid static config and atomically swaps it only if the rebuild succeeds.

If a Service is deleted or becomes invalid, the next complete replacement removes its route from the active snapshot after rebuild succeeds. Duplicate discovered hosts disable all discovered routes for that host.

If the watch stream fails after the first successful runtime sync, the supervisor retries the one-shot controller. Each retry performs a fresh Service list and opens a new watch from the new list resource version. During retry backoff, the latest successful discovered route set and the active runtime snapshot remain in place. If the relist returns a legitimately empty namespace, that empty discovered route set is applied after the rebuild succeeds.

## Reload Interaction

`SIGHUP` reload does not re-run the Kubernetes API initial list and does not restart the watch controller. Reload reads the static config file, validates it, and re-merges it with the latest discovered routes already held by the runtime. A watch update re-merges the latest discovered routes with the latest valid static config.

Reload and watch updates are serialized by the server's route snapshot update lock. Active connections keep the snapshot they loaded at connection start. New connections use the latest successfully swapped snapshot.

## Duplicate Host Policy

Duplicate discovered hosts are unsafe because a controller cannot reliably infer the operator's intended backend. If the same host is discovered more than once, every discovered route for that host is disabled and reported with a duplicate reason. The project avoids first-wins behavior for discovered routes.

The controller core applies this policy to the discovered route set before returning a snapshot. Duplicate hosts are returned in deterministic order, and the affected resources are reported with `duplicate_host`.

## Controller Core

`BuildDiscoveredRoutes` is the pure ServiceInput-to-Result boundary:

```text
[]ServiceInput -> Result
```

It uses the Service annotation parser, collects skipped resources by low-cardinality reason, disables duplicate discovered hosts, and returns routes in deterministic order. Invalid resources do not fail the whole snapshot; the builder returns the best valid discovered route set plus skip information in one complete `Result`.

This controller core is used by both startup discovery and runtime watch updates. Startup uses the `Result` for initial-list logging and converts `Result.Routes` to a `SnapshotProvider` before rebuilding the initial route snapshot. Runtime watch updates pass the complete `Result` to the runtime sink, which converts `Result.Routes` to a `SnapshotProvider` before calling `UpdateDiscoveredRoutes`. Runtime discovery metrics track sync, watch retry, rebuild health, and skipped Service counts from the latest successfully applied runtime `Result`.

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

Watch setup is part of startup when Kubernetes discovery is enabled. If the watch controller cannot complete its initial list, open the watch, and publish the first runtime sync, startup fails.

After the watch controller has started, watch error events or unexpected watch channel close are retryable. The supervisor logs `kubernetes_discovery_watch_retry_scheduled`, waits with exponential backoff, then relists Services and starts a new watch. The default backoff is fixed in code: initial delay `1s`, factor `2.0`, and max delay `30s`. It is not configurable in YAML yet. The existing active route snapshot remains in place while retrying.

Context cancellation is a normal stop and is not retried. Invalid Service annotations and duplicate discovered hosts are route-level skips, not supervisor restart reasons.

If a watch update reaches the runtime but `RouteSnapshot` rebuild fails, the gateway logs `kubernetes_discovery_runtime_snapshot_failed` and keeps the existing active snapshot. Invalid Service annotations and duplicate discovered hosts are handled by the discovery controller as skipped resources; valid discovered routes can still update the active snapshot.

## RBAC

The repository includes a namespace-scoped RBAC example for Kubernetes Service discovery:

```text
deploy/kubernetes/discovery-rbac.yaml
```

With startup initial list and the Service watch controller core, the ServiceAccount used by the gateway needs only these namespace-scoped permissions:

- `get`, `list`, `watch` on `services`

EndpointSlice, Pod, Secret, or cluster-wide permissions should be added only if a later implementation actually needs them.

All-namespaces discovery is not implemented. Supporting it later would require an explicit ClusterRole/ClusterRoleBinding design and separate review.

Reading the ServiceAccount namespace file is not a Kubernetes API call and is not controlled by Kubernetes RBAC. It depends on the Pod's projected ServiceAccount volume. The gateway does not need permissions for Secrets and must not read the ServiceAccount token for namespace resolution.

## Metrics

When `metrics.enabled` is true, Kubernetes discovery exposes these Prometheus metrics:

```text
mc_gateway_kubernetes_discovered_routes
mc_gateway_kubernetes_watch_restarts_total{reason}
mc_gateway_kubernetes_watch_running
mc_gateway_kubernetes_last_successful_sync_timestamp_seconds
mc_gateway_kubernetes_skipped_services{reason}
mc_gateway_kubernetes_discovery_errors_total{reason}
```

Metric meanings:

- `mc_gateway_kubernetes_discovered_routes`: number of discovered routes in the latest successfully applied runtime snapshot.
- `mc_gateway_kubernetes_watch_restarts_total{reason}`: watch supervisor retry/restart count after runtime watch failures.
- `mc_gateway_kubernetes_watch_running`: `1` while the runtime watch supervisor is running, otherwise `0`.
- `mc_gateway_kubernetes_last_successful_sync_timestamp_seconds`: Unix timestamp of the last successful discovery sync applied to runtime routing.
- `mc_gateway_kubernetes_skipped_services{reason}`: number of Kubernetes Services skipped in the latest successfully applied runtime discovery snapshot by bounded skip reason.
- `mc_gateway_kubernetes_discovery_errors_total{reason}`: discovery/runtime errors by bounded reason.

`mc_gateway_kubernetes_skipped_services{reason}` uses only these reason values:

- `disabled`
- `invalid_annotation_prefix`
- `invalid_service_name`
- `invalid_namespace`
- `missing_host`
- `invalid_host`
- `missing_port`
- `invalid_port`
- `port_not_found`
- `duplicate_host`
- `unknown`

`mc_gateway_kubernetes_watch_restarts_total{reason}` uses only these reason values:

- `list_failed`
- `watch_closed`
- `watch_error`
- `watch_setup_failed`
- `unknown`

`mc_gateway_kubernetes_discovery_errors_total{reason}` uses only these reason values:

- `namespace_resolution_failed`
- `incluster_config_failed`
- `initial_list_failed`
- `watch_error`
- `watch_closed`
- `watch_setup_failed`
- `rebuild_failed`
- `unknown`

Metrics are disabled by default with the rest of the metrics endpoint. When metrics are disabled, discovery instrumentation is a no-op and is safe to call.

Discovery metrics stay low-cardinality. Do not use namespace, Service name, host, backend, annotation value, Kubernetes resource version, or raw error message as metric labels. Keep labels to bounded values such as `reason`.

Invalid Service annotations and duplicate discovered hosts are not counted in `mc_gateway_kubernetes_discovery_errors_total`; they are route-level skips reported through `mc_gateway_kubernetes_skipped_services{reason}` after a runtime discovery snapshot is successfully applied.

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

This interface allows the gateway runtime to fetch a snapshot of discovered routes without knowing the details of the underlying discovery source. It is intentionally route-only: skipped Services, duplicate host metadata, and skipped reason counts remain discovery `Result`, logging, or metrics concerns.

### Memory Provider

A `MemoryProvider` is implemented for tests. It stores a list of discovered routes in memory and returns a safe copy when requested.

### Kubernetes Snapshot Provider

Kubernetes discovery uses `SnapshotProvider` to expose `Result.Routes` through `RouteProvider`. `NewSnapshotProviderFromResult(result)` copies only the route slice into the provider. It does not expose `Result.Skipped`, `Result.DuplicateHosts`, or `Result.SkippedByReason` through the runtime merge path.

### Kubernetes Service Initial List

The project includes `client-go` dependency and the startup initial-list implementation for Kubernetes Services.

- `ServiceLister` interface: abstracts listing Services and converting them to `ServiceInput`.
- `ClientServiceLister`: implementation using `client-go`.
- `ToServiceInput`: pure conversion from `corev1.Service` to `ServiceInput`.

This implementation fetches a one-time snapshot of Services from the Kubernetes API at startup when Kubernetes discovery is enabled. It prepares Services for `BuildDiscoveredRoutes`, converts `Result.Routes` through `SnapshotProvider`, and feeds that route-only provider into the startup route snapshot rebuild. After the rebuild succeeds, it logs initial-list result stats. The gateway then starts the Service watch controller for runtime updates.

### Kubernetes Service Watch Controller Core

The project includes a namespace-scoped Service watch controller core. It performs an initial Service list, starts a direct `client-go` watch from the returned resource version, tracks the current Service object set, rebuilds a complete discovery `Result` after Service add/update/delete events, and publishes that complete replacement to a route sink.

The controller uses direct watch instead of an informer in this first runtime integration because it keeps the API small and makes fake-client tests explicit: list, watch events, and sink updates are all visible at the controller boundary. Initial list failure, watch setup failure, watch error events, and unexpected watch channel close return errors to the controller caller.

The gateway runtime owns the active `RouteSnapshot` swap. The watch controller only publishes complete discovered route replacements to a sink. When the sink accepts full results, runtime converts `Result.Routes` through `SnapshotProvider` before calling `UpdateDiscoveredRoutes`.

### Kubernetes Watch Supervisor

The runtime wraps the one-shot Service watch controller in a supervisor. Before the first successful sync, controller errors are startup errors. After the first successful sync, controller errors are treated as retryable watch failures. The supervisor waits with exponential backoff and then starts a fresh one-shot controller run, which relists Services and opens a new watch.

The supervisor does not clear the runtime sink or active snapshot during retry. It resumes runtime updates only after a retry run successfully publishes a new complete discovered route set. Context cancellation stops the supervisor without retry.

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
- Pure controller core that builds discovery `Result` snapshots from `ServiceInput` values.
- In-memory merge builder that combines static and discovered explicit routes.
- Internal `RouteProvider` interface, test `MemoryProvider`, and Kubernetes `SnapshotProvider`.
- `client-go` dependency added.
- Startup `ServiceLister` Kubernetes API initial list.
- `ToServiceInput` conversion from Kubernetes `corev1.Service`.
- Current namespace resolution helper for `discovery.kubernetes.namespace == ""`.
- `RebuildRouteSnapshot` helper for unified static/discovered route management.
- Runtime route snapshot boundary that receives startup-discovered routes through `SnapshotProvider` when Kubernetes discovery is enabled.
- Runtime route snapshot updates from the namespace-scoped Service watch controller.
- Retry/backoff supervisor for runtime Kubernetes watch failures.
- Low-cardinality Kubernetes discovery metrics, including skipped Service counts by bounded reason.
- Duplicate discovered host helper tests.
- Documentation of the current merge and operation policy.

Not implemented yet:

- Periodic resync.
- ClusterRole or all-namespaces RBAC manifests.
- CRDs.
- Wake-up or scale-to-zero controller behavior.
- REST API or Web UI.
- Startup skipped Service metrics.

## Current Status

Current implementation is config, parser, pure controller core, merge-builder, route-only provider interface, Kubernetes snapshot provider, current namespace helper, startup client-go initial list, namespace-scoped Service watch controller core, runtime merge-boundary integration, runtime route snapshot updates after Service watch events, and retry/backoff recovery for runtime watch failures.
