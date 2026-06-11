# Simple Voice Chat Development

This document defines the local research environment for future Simple Voice Chat support. `mc-router` supports Minecraft TCP routing and a fixed-backend UDP relay foundation for local transport validation. Dynamic, Transfer-aware Simple Voice Chat routing remains deferred.

## Architecture Under Test

Fixed Hub relay topology available in this environment:

```text
Minecraft client
  ├─ TCP 25565
  │      ↓
  │   mc-router TCP hostname router
  │      ↓
  │   Hub backend
  └─ UDP 24454
         ↓
      mc-router fixed UDP relay
         ↓
      Hub Simple Voice Chat UDP 24454
```

Intended eventual dynamic topology, not implemented yet:

```text
Minecraft clients
  ├─ TCP 25565
  └─ UDP 24454
         ↓
      mc-router
         ├─ Hub backend
         └─ Secondary backend
```

The UDP relay currently maps one public UDP listener to one explicitly configured backend endpoint. It does not inspect Simple Voice Chat packets, parse encrypted payloads, associate UDP sessions with Minecraft TCP sessions, route by hostname or player identity, or switch backends after Minecraft Transfer.

The fixed relay identifies a transport session by the full client UDP endpoint, `client IP + client UDP source port`. This is only a UDP transport mapping for one fixed backend. It is not sufficient for future dynamic backend selection, and UDP source-address spoofing cannot be fully prevented at this layer. Production deployments still need appropriate network filtering.

Current local ports:

- `25565/tcp`: `mc-router` public Minecraft TCP entry point.
- `24454/udp`: `mc-router` fixed UDP relay to Hub Simple Voice Chat.
- `24455/udp`: direct Hub Simple Voice Chat baseline that bypasses `mc-router`.
- `24456/udp`: direct backend Simple Voice Chat baseline.
- `25566/tcp` and `25567/tcp`: direct backend TCP debugging ports.
- `25575/tcp` and `25576/tcp`: local RCON ports for Hub and backend.

Simple Voice Chat is installed on both backend servers through `itzg/minecraft-server` Modrinth auto-downloads, pinned to Modrinth version ID `7ROzE7Qh` (`voicechat-bukkit-2.6.18.jar`). The JAR is downloaded into the server data volume at runtime and must not be committed. The Hub advertises `hub.mc-router.test:24454`, which is handled by the fixed UDP relay. The direct Hub baseline remains reachable through host UDP `24455`.

## Local Prerequisites

- Docker.
- Docker Compose.
- Go.
- A Minecraft Java Edition client compatible with Minecraft `1.21.1`.
- The matching Simple Voice Chat client mod.
- `tcpdump` or Wireshark for packet capture.
- Optional Linux `tc`/`netem` tooling for packet loss, latency, jitter, and reordering tests.

Starting the test servers requires accepting the Minecraft EULA. Set `MC_ROUTER_ACCEPT_MINECRAFT_EULA=TRUE` only if you accept that EULA for the local run.

## Startup And Shutdown

Build the local `mc-router` image:

```powershell
docker build -t mc-gateway:voicechat-dev .
```

Start the environment:

```powershell
$env:MC_ROUTER_ACCEPT_MINECRAFT_EULA = "TRUE"
docker compose -f compose.voicechat.yaml up --build
```

On Linux or macOS:

```bash
MC_ROUTER_ACCEPT_MINECRAFT_EULA=TRUE docker compose -f compose.voicechat.yaml up --build
```

Inspect logs from another terminal:

```powershell
docker compose -f compose.voicechat.yaml logs -f mc-router hub backend
```

Inspect only the UDP relay lifecycle and session logs:

```powershell
docker compose -f compose.voicechat.yaml logs -f mc-router | Select-String "udp_relay"
```

On Linux or macOS:

```bash
docker compose -f compose.voicechat.yaml logs -f mc-router | grep udp_relay
```

Inspect UDP relay metrics:

```powershell
Invoke-WebRequest http://127.0.0.1:9090/metrics | Select-Object -ExpandProperty Content | Select-String "mc_gateway_udp_"
```

On Linux or macOS:

```bash
curl -s http://127.0.0.1:9090/metrics | grep mc_gateway_udp_
```

Stop the environment without deleting server state:

```powershell
docker compose -f compose.voicechat.yaml down
```

Stop and reset only the test server state:

```powershell
docker compose -f compose.voicechat.yaml down --volumes
```

The named volumes are `mc-router-voicechat-hub-data` and `mc-router-voicechat-backend-data`.

## Hostname Setup

Resolve the local test hostnames to the Docker host. No public DNS is required.

On Windows, edit `C:\Windows\System32\drivers\etc\hosts` as Administrator:

```text
127.0.0.1 hub.mc-router.test
127.0.0.1 backend.mc-router.test
```

On Linux or macOS, edit `/etc/hosts`:

```text
127.0.0.1 hub.mc-router.test
127.0.0.1 backend.mc-router.test
```

Use the same hostnames in the Minecraft client server list. `hub.mc-router.test:25565` routes to the Hub through `mc-router`; `backend.mc-router.test:25565` routes to the second backend through the same public TCP port.

To trigger a Minecraft Transfer from the Hub to the backend through RCON, run:

```powershell
docker compose -f compose.voicechat.yaml exec hub rcon-cli transfer backend.mc-router.test 25565 <player-name>
```

If the server version changes, verify the `transfer` command syntax before relying on this step.

## Packet Capture Procedure

Host-level capture for the fixed shared public voice port:

```bash
sudo tcpdump -ni any udp port 24454 -w voicechat.pcap
```

Direct backend baseline captures:

```bash
sudo tcpdump -ni any "udp port 24455 or udp port 24456" -w voicechat-direct-backends.pcap
```

Container-network capture with the optional tools profile:

```powershell
docker compose -f compose.voicechat.yaml --profile nettools up -d hub-nettools backend-nettools
docker compose -f compose.voicechat.yaml exec hub-nettools tcpdump -ni any udp port 24454 -w /tmp/hub-voicechat.pcap
docker compose -f compose.voicechat.yaml cp hub-nettools:/tmp/hub-voicechat.pcap ./voicechat-captures/hub-voicechat.pcap
```

Packet captures may contain client IP addresses, LAN addressing, and timing information. Do not commit `.pcap` files or capture directories.

Inspect endpoints:

```bash
tcpdump -nn -r voicechat-direct-backends.pcap
```

In Wireshark, compare direct-backend traffic against fixed-relay traffic by checking source IP, source UDP port, destination IP, destination UDP port, packet timing, and session changes around Transfer or reconnect events. Do not infer protocol fields from encrypted-looking payloads until upstream source inspection or packet captures confirm what is visible.

## Fixed Hub Relay Manual Checklist

### Fixed Hub relay test

1. Build and start Compose.
2. Connect one real Minecraft client with the Simple Voice Chat client mod to `hub.mc-router.test:25565`.
3. Confirm the client reports Voice Chat connected.
4. Confirm UDP packets pass through `mc-router` on host UDP `24454`.
5. Connect a second real client to the Hub and confirm speaking/listening works.
6. Confirm `mc_gateway_udp_packets_total`, `mc_gateway_udp_bytes_total`, and `mc_gateway_udp_sessions` change in the metrics endpoint.

### Same-IP test

1. Connect two clients from the same machine or LAN.
2. Confirm `mc-router` debug logs show distinct UDP session endpoints with different client source ports.
3. Confirm no audio or backend reply crosses between clients.

### Restart test

1. Restart the Hub:

   ```powershell
   docker compose -f compose.voicechat.yaml restart hub
   ```

2. Confirm stale UDP sessions expire from `mc-router`.
3. Confirm clients can reconnect without restarting `mc-router`.

### Router restart test

1. Restart `mc-router`:

   ```powershell
   docker compose -f compose.voicechat.yaml restart mc-router
   ```

2. Confirm clients can establish new Voice Chat sessions.
3. Record whether the Simple Voice Chat client reconnects automatically or requires a manual reconnect.

### Fault test

Using the existing scoped nettools environment, test packet loss, latency, jitter, and temporary backend UDP unavailability. Expected relay behavior is transport-level only: packets may be delayed or dropped by the injected fault, stale sessions expire after the configured idle timeout, and new client packets create fresh sessions when the backend is reachable. Do not treat this as a Simple Voice Chat protocol guarantee.

## Research Questions

Verify these facts before designing protocol-aware UDP routing:

- Who sends the first UDP packet: client or backend?
- Is a stable session identifier visible in UDP packets?
- If an identifier exists, is it encrypted or otherwise opaque?
- Does the client UDP source port change after Minecraft Transfer?
- Does the backend send a new secret after Transfer?
- How do reconnects differ from initial sessions?
- Can one client have overlapping old and new voice sessions?
- Do two clients behind the same NAT remain distinguishable?
- How long do stale backend sessions remain active?
- How does backend restart affect the voice session?
- Which behavior is controlled by Minecraft plugin messages rather than UDP?

Record evidence from packet captures, server logs, and upstream source references for each answer.

## Fault-Injection Procedure

Start optional network tooling:

```powershell
docker compose -f compose.voicechat.yaml --profile nettools up -d hub-nettools backend-nettools
```

Apply packet loss to the Hub container network namespace:

```powershell
docker compose -f compose.voicechat.yaml exec hub-nettools tc qdisc add dev eth0 root netem loss 10%
```

Apply latency and jitter:

```powershell
docker compose -f compose.voicechat.yaml exec hub-nettools tc qdisc replace dev eth0 root netem delay 150ms 40ms distribution normal
```

Apply packet reordering where the local kernel supports it:

```powershell
docker compose -f compose.voicechat.yaml exec hub-nettools tc qdisc replace dev eth0 root netem delay 80ms reorder 25% 50%
```

Remove fault injection:

```powershell
docker compose -f compose.voicechat.yaml exec hub-nettools tc qdisc del dev eth0 root
```

Run the same commands against `backend-nettools` to affect only the backend container namespace. Avoid applying `tc` directly to normal host interfaces unless you are intentionally testing the whole host network and know how to revert it.

Backend UDP unavailability:

```powershell
docker compose -f compose.voicechat.yaml stop backend
```

Backend restart:

```powershell
docker compose -f compose.voicechat.yaml restart backend
```

`mc-router` restart:

```powershell
docker compose -f compose.voicechat.yaml restart mc-router
```

Client reconnect:

Disconnect the Minecraft client, wait for voice session cleanup evidence in logs or captures, and reconnect to the same hostname.

## Manual E2E Matrix

| Test | Setup | Action | Expected result | Evidence to collect |
| --- | --- | --- | --- | --- |
| One client on Hub through fixed relay | Start Compose, client has Simple Voice Chat mod | Connect to `hub.mc-router.test:25565` | TCP route reaches Hub; fixed Hub voice relay connects through `24454/udp` | Client UI, Hub logs, router UDP logs, UDP metrics |
| Two clients on Hub through fixed relay | Two modded clients on Hub | Both join Hub | Both are present; full UDP endpoint sessions are distinct | Client UI, Hub logs, endpoint tuples, router UDP logs |
| Direct Hub voice baseline | Start Compose and capture `24455/udp` | Connect to Hub direct TCP debug port if needed | Direct Hub voice path bypasses `mc-router` | Client UI, Hub logs, UDP capture |
| Direct backend voice connection | Start backend and capture `24456/udp` | Connect to `backend.mc-router.test:25565` | TCP route reaches backend; direct backend voice baseline connects | Backend logs, UDP capture |
| Both clients speaking bidirectionally | Two clients on same backend | Speak from each client | Audio is bidirectional on direct baseline | Client observations, captures |
| One client transfers to backend | One client on Hub | Run Hub RCON `transfer` command | Client reconnects to backend TCP route | RCON output, router logs, captures before/after |
| Both clients transfer to backend | Two clients on Hub | Transfer both clients | Both clients reach backend and voice remains backend-local | Router logs, backend logs, captures |
| Client returns to Hub | Client on backend | Transfer or reconnect to Hub | Client reaches Hub and voice baseline returns to Hub UDP port | Router logs, captures |
| Backend restart | Client on backend | Restart `backend` service | TCP/voice behavior is observed and documented | Logs, client UI, captures |
| mc-router restart | Client connected through router | Restart `mc-router` service | TCP behavior is observed; new fixed UDP relay sessions can be established | Router logs, client result |
| Client reconnect | Client connected to Hub or backend | Disconnect and reconnect | New session behavior is documented | Logs, endpoint tuples |
| Two clients from same public IP or LAN | Two clients behind same NAT/LAN | Both speak on same backend | Sessions remain distinguishable on direct baseline | Endpoint tuples, audio result |
| Stale session cleanup | Client disconnects unexpectedly | Wait for server cleanup | Stale session lifetime is measured | Logs, capture timestamps |
| Invalid UDP traffic | Capture running | Send malformed UDP to direct backend and fixed router relay port | No process crashes; behavior is recorded | Logs, exit status, capture |
| Sustained voice traffic | Two clients connected | Speak intermittently for at least 30 minutes | No unbounded growth or session confusion is observed | Logs, captures, resource notes |

## Production-Readiness Gate

Simple Voice Chat support must not be called complete until all of these are satisfied:

- One public UDP port.
- Correct backend selection.
- No cross-backend audio leakage.
- No same-NAT client collisions.
- Correct behavior after Minecraft Transfer.
- Automatic voice reconnect or documented supported reconnection behavior.
- Bounded session lifetime.
- Graceful shutdown.
- Backend restart recovery.
- `mc-router` restart recovery.
- Malformed packets cannot crash the process.
- Race detector passes.
- Metrics are bounded and low-cardinality.
- Unit tests pass.
- Integration tests pass.
- Real two-client E2E tests pass.
- Docker deployment works.
- Kubernetes deployment is later validated separately.

Until those gates pass, keep Simple Voice Chat listed as deferred.
