# Architecture diagrams

This page provides diagram-as-code views of the main `mc-router` routing and control boundaries.

The diagrams are intentionally high-level. [`architecture.md`](architecture.md) remains the detailed source of truth for edge cases, failure handling, timeouts, fallback semantics, and implementation-specific behavior.

## High-level routing

```mermaid
flowchart LR
    Client["Minecraft client"] -->|"public TCP :25565"| Router["mc-router / mc-gateway"]
    Router -->|"hub.example.com"| Hub["Hub backend"]
    Router -->|"smp.example.com"| SMP["SMP backend"]
    Router -->|"creative.example.com"| Creative["Creative backend"]
```

Several public DNS names can point to the same listener. The hostname from the Minecraft handshake selects the internal backend.

## Java connection flow

```mermaid
flowchart TD
    Connect["Client opens TCP connection"] --> Handshake["Read first Minecraft handshake"]
    Handshake --> Parse["Parse serverAddress and next state"]
    Parse --> Normalize["Normalize requested host"]
    Normalize --> Route["Select route from active snapshot"]
    Route --> State{"Requested state"}

    State -->|"STATUS"| Status{"STATUS routing controls"}
    State -->|"LOGIN / Transfer"| Backend["Normal backend"]

    Status -->|"statusOverride"| Override["Return router-generated STATUS"]
    Status -->|"statusBackend"| StatusBackend["Connect to status backend"]
    Status -->|"neither"| Backend

    StatusBackend --> ForwardStatus["Forward original handshake"]
    Backend --> Forward["Forward original handshake"]
    ForwardStatus --> ProxyStatus["Proxy TCP in both directions"]
    Forward --> Proxy["Proxy TCP in both directions"]
```

Normal Java routing inspects only the initial handshake needed for routing. After backend selection, the remaining connection is proxied as TCP traffic.

## STATUS routing precedence

```mermaid
flowchart TD
    Status["Java STATUS connection"] --> Override{"statusOverride configured?"}
    Override -->|"yes"| Local["Return router-generated STATUS"]
    Override -->|"no"| StatusBackend{"statusBackend configured?"}
    StatusBackend -->|"yes"| Dedicated["Proxy to statusBackend"]
    StatusBackend -->|"no"| Normal["Proxy to backend"]

    Login["LOGIN / Transfer"] --> Normal
```

The explicit precedence is `statusOverride -> statusBackend -> backend`. LOGIN and Minecraft Transfer traffic continue to use the normal backend.

## Route snapshot updates

```mermaid
flowchart LR
    Static["Static YAML routes"] --> Rebuild["Rebuild route snapshot"]
    Discovery["Kubernetes discovery result"] --> Provider["SnapshotProvider"]
    Provider --> Rebuild

    Reload["SIGHUP reload"] --> Static
    Watch["Kubernetes Service watch"] --> Discovery

    Rebuild -->|"success"| Active["Atomically replace active snapshot"]
    Rebuild -->|"failure"| Preserve["Keep previous active snapshot"]

    Connection["New connection"] --> Active
```

Static configuration and Kubernetes discovery feed the same route-snapshot model. A failed rebuild preserves the previously active snapshot, while new connections use the latest successfully published snapshot.

## Routing and control-plane boundary

```mermaid
flowchart LR
    Connection["Minecraft connection"] --> Decision["Route decision"]
    Decision --> Dial["Backend dial"]
    Dial --> Proxy["Minecraft traffic proxy"]

    Decision -. "optional event" .-> Integration["External scaler / monitoring integration"]
    Integration --> Controller["External workload controller / observability"]
```

Optional notifications and monitoring integrations are deliberately outside routing correctness. Their state does not implicitly redefine LOGIN, Transfer, or ordinary STATUS routing.
