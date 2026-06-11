# Transfer-Aware Simple Voice Chat Routing Design

This document records the investigation for dynamic Simple Voice Chat routing
through one public UDP endpoint. It does not declare production support.

Dynamic and Transfer-aware Simple Voice Chat routing remains under development.
The existing fixed-backend UDP relay remains the only implemented UDP relay mode.

## Scope

Required target topology:

```text
client UDP play.alec-ofc.com:24454
  -> mc-router shared UDP listener
  -> current backend Simple Voice Chat UDP listener
```

The current backend must follow the Minecraft backend selected for the player,
including after Minecraft Transfer. Unknown or ambiguous UDP sessions must be
dropped instead of falling back to Hub or another unrelated backend.

## Sources Inspected

Simple Voice Chat source inspected:

- Repository: <https://github.com/henkelmax/simple-voice-chat>
- Commit: `68b283b2460199ee0bf9f30321ac894df730ad7d`
- Version metadata: `mod_version=2.6.18+26.1.2`
- Compatibility version: `voicechat_compatibility_version=20`

Primary upstream source files inspected:

- `common/src/main/java/de/maxhenkel/voicechat/voice/common/NetworkMessage.java`
- `common-client/src/main/java/de/maxhenkel/voicechat/voice/client/ClientNetworkMessage.java`
- `common/src/main/java/de/maxhenkel/voicechat/voice/common/AuthenticatePacket.java`
- `common/src/main/java/de/maxhenkel/voicechat/voice/common/Secret.java`
- `common/src/main/java/de/maxhenkel/voicechat/voice/server/Server.java`
- `common/src/main/java/de/maxhenkel/voicechat/voice/server/ServerVoiceEvents.java`
- `common/src/main/java/de/maxhenkel/voicechat/net/SecretPacket.java`
- `common/src/main/java/de/maxhenkel/voicechat/net/RequestSecretPacket.java`
- `common-proxy/src/main/java/de/maxhenkel/voicechat/VoiceProxy.java`
- `common-proxy/src/main/java/de/maxhenkel/voicechat/network/VoiceProxyServer.java`
- `common-proxy/src/main/java/de/maxhenkel/voicechat/network/VoiceProxyBridgeManager.java`
- `common-proxy/src/main/java/de/maxhenkel/voicechat/sniffer/VoiceProxySniffer.java`
- `common-proxy/src/main/java/de/maxhenkel/voicechat/sniffer/SniffedSecretPacket.java`
- `velocity/src/main/java/de/maxhenkel/voicechat/SimpleVoiceChatVelocity.java`
- `bungeecord/src/main/java/de/maxhenkel/voicechat/SimpleVoiceChatBungeecord.java`
- `api/src/main/java/de/maxhenkel/voicechat/api/events/VoiceHostEvent.java`
- `api/src/main/java/de/maxhenkel/voicechat/api/events/PlayerConnectedEvent.java`
- `api/src/main/java/de/maxhenkel/voicechat/api/events/PlayerDisconnectedEvent.java`

mc-router source files inspected:

- `internal/mcproto/handshake.go`
- `internal/proxy/server.go`
- `internal/router/router.go`
- `internal/config/config.go`
- `internal/udprelay/relay.go`
- `internal/metrics/recorder.go`
- `cmd/mc-gateway/main.go`

Git history inspected:

- Transfer intent support: `fb5b7a6`
- Local Simple Voice Chat research environment: PR `#57`
- Fixed-backend UDP relay foundation: PR `#58`

## Verified Facts

### mc-router Visibility

- Verified from source: mc-router parses only the initial Minecraft handshake
  packet before selecting a backend.
- Verified from source: mc-router recognizes status, login, and Java 1.20.5+
  transfer intent states from the initial handshake.
- Verified from source: after selecting a backend, mc-router writes the original
  handshake to the backend and proxies both TCP directions as raw bytes.
- Verified from source: mc-router does not parse successful login, encryption
  negotiation, encrypted login/configuration/play packets, or plugin messages.
- Verified from source: mc-router does not know a player's UUID or username for
  successful proxied connections.
- Verified from source: mc-router's Transfer support uses the transfer
  handshake target address only to choose the new TCP backend; it does not carry
  player identity into UDP routing.

### Simple Voice Chat Minecraft-Side Setup

- Verified from source: the client sends a Minecraft plugin message on
  `voicechat:request_secret` containing its compatibility version.
- Verified from source: the backend replies with a plugin message on
  `voicechat:secret`.
- Verified from source: the secret packet contains a 16-byte voice secret, the
  backend voice UDP port, the player UUID, codec/configuration values, keepalive
  settings, and a voice host string.
- Verified from source: backend plugins can use `VoiceHostEvent` to replace the
  voice host sent to the client.
- Verified from source: backend plugins receive `PlayerConnectedEvent` after a
  voice UDP connection is established and `PlayerDisconnectedEvent` when it is
  removed.
- Inferred but not yet verified locally: a backend companion can use the public
  API events to register player UUID to backend route mappings and unregister
  them on disconnect, but exact event timing must be validated with real
  clients.

### Simple Voice Chat UDP

- Verified from source: client-to-server UDP datagrams begin with magic byte
  `0xff`, then a clear player UUID, then a length-prefixed encrypted payload.
- Verified from source: server-to-client UDP datagrams begin with magic byte
  `0xff`, then a length-prefixed encrypted payload; they do not include a clear
  player UUID.
- Verified from source: the encrypted payload contains the packet type and body.
  The authentication packet body includes the player UUID and the voice secret,
  but both are inside AES-GCM encrypted payload data.
- Verified from source: a server accepts a UDP packet only if it has a stored
  secret for the clear player UUID and the encrypted payload decrypts with that
  secret.
- Verified from source: voice audio packets are encrypted payloads. mc-router
  must not inspect them.
- Verified from source: the clear UUID in client-to-server UDP is a stable
  routing identifier only if mc-router already has an authenticated mapping from
  that UUID to an allowed backend.
- Inferred but not yet verified by packet capture: after Transfer, Simple Voice
  Chat performs a new secret request/secret response with the new backend.

### Official Proxy Support

- Verified from source: official Velocity and Bungee support is not passive TCP
  forwarding. It registers plugin message channels and intercepts voicechat
  secret/request-secret messages.
- Verified from source: official proxy support rewrites the voice host/port in
  the secret packet so clients connect to the proxy UDP listener.
- Verified from source: official proxy support maps voice sessions to the
  player's current backend using Velocity/Bungee player server state.
- Verified from source: official proxy support closes a player's UDP bridge when
  the player disconnects from a backend or switches backend.

### Local Runtime and Packet Capture Status

- Verified from local runtime test: not yet performed for dynamic routing.
- Verified from packet capture: not yet performed for dynamic routing.
- Verified from local runtime test: fixed-backend UDP relay support exists in
  `main` and is outside the scope of this dynamic design.

## Visibility Matrix

| Stage | Hostname | Username | UUID | Client IP | Client TCP port | UDP endpoint | Voice secret | Voice session ID | Current backend | Target backend |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| TCP handshake | yes, source-verified | no | no | yes | yes | no | no | no | no | yes after route match |
| status | yes, source-verified | no | no | yes | yes | no | no | no | no | yes after route match |
| login start | not parsed on successful proxy path | not parsed | no | yes | yes | no | no | no | no | yes after route match |
| encryption negotiation | no, raw TCP proxy | no | no | yes | yes | no | no | no | no | previously selected backend only |
| encrypted login | no, raw TCP proxy | no | no | yes | yes | no | no | no | no | previously selected backend only |
| configuration phase | no, raw TCP proxy | no | no | yes | yes | no | no | no | no | previously selected backend only |
| play phase | no, raw TCP proxy | no | no | yes | yes | no | no | no | no | previously selected backend only |
| transfer intent | yes, source-verified | no | no | yes | yes | no | no | no | no | yes after route match |
| UDP authentication | no | no | yes in clear UDP UUID, source-verified | yes | no | yes | no, encrypted | clear UUID can act as lookup key with prior registration | no, unless registered | no, unless registered |
| UDP voice traffic | no | no | yes in client-to-server only, source-verified | yes | no | yes | no | clear UUID can act as lookup key with prior registration | no, unless existing session/registration | no, unless existing session/registration |

The UUID visible in UDP is not enough by itself. It must be bound to an allowed
backend by an authenticated, non-public control path.

## Candidate Architectures

### 1. Transparent mc-router-only dynamic routing

- Correctness: rejected. mc-router cannot see successful login identity or
  Simple Voice Chat plugin messages.
- Security: unsafe without guessing from IP, UDP port, or last TCP route.
- Same-NAT behavior: not safe if routing by IP.
- Transfer behavior: not safe because transfer intent has no visible UUID.
- Operational complexity: low, but incorrect.
- Compatibility: preserves one public UDP port.
- Failure modes: unknown sessions would require unsafe fallback or packet loss.
- Backend plugins required: no.
- Client changes required: no.

### 2. Protocol-aware UDP routing using only a visible Voice Chat identifier

- Correctness: incomplete. The client-to-server UDP UUID is visible, but
  mc-router still needs a verified UUID-to-backend mapping.
- Security: safe only with authenticated registration; unsafe if the UUID is
  used with a guessed backend.
- Same-NAT behavior: safe when combined with transport sessions and UUID mapping.
- Transfer behavior: possible only if mappings are updated on backend switch.
- Operational complexity: moderate.
- Compatibility: preserves one public UDP port.
- Failure modes: unknown or expired UUIDs must be dropped.
- Backend plugins required: yes, unless a real Minecraft proxy provides state.
- Client changes required: no.

### 3. Lightweight backend companion plugin or mod

- Correctness: selected architecture. Each backend observes voice setup and
  registers UUID-to-backend ownership with mc-router over an internal
  authenticated API.
- Security: can be safe if the control API is bound internally, authenticated
  with a secret from the environment or Kubernetes Secret, constant-time token
  comparison is used, and registrations can name only predefined backends.
- Same-NAT behavior: safe because UDP transport sessions remain keyed by full
  UDP endpoint and backend selection is keyed by registered UUID.
- Transfer behavior: safe if new backend registration replaces old ownership and
  mc-router closes stale UDP sessions for that UUID.
- Operational complexity: higher than transparent routing, lower than adopting a
  full external proxy.
- Compatibility: preserves one public UDP port and requires no client changes.
- Failure modes: if registration is absent or expired, UDP is dropped; if the
  companion is down, voice fails closed.
- Backend plugins required: yes.
- Client changes required: no.

### 4. mc-router internal registration API

- Correctness: necessary part of architecture 3.
- Security: must not be public; must use authenticated requests and allow only
  configured backend IDs.
- Same-NAT behavior: safe when registrations are per UUID and UDP sessions are
  per client endpoint.
- Transfer behavior: supports explicit backend reassignment.
- Operational complexity: moderate.
- Compatibility: preserves one public UDP port.
- Failure modes: stale mappings expire; unknown registrations are rejected.
- Backend plugins required: yes.
- Client changes required: no.

### 5. Official Simple Voice Chat proxy integration

- Correctness: proven by upstream design when running on Velocity or Bungee.
- Security: uses the proxy's authenticated player/backend state and plugin
  message participation.
- Same-NAT behavior: safe.
- Transfer behavior: supported by the proxy's server-switch lifecycle.
- Operational complexity: high if mc-router remains the public gateway.
- Compatibility: preserves one public UDP port at the official proxy.
- Failure modes: depends on the proxy runtime and its configuration.
- Backend plugins required: official proxy integration plus backend setup.
- Client changes required: no.

### 6. Add Velocity in front of or behind mc-router

- Correctness: feasible if Velocity becomes the component that owns player
  identity, server switching, and Simple Voice Chat proxy behavior.
- Security: acceptable when configured as a real Minecraft proxy with online
  authentication preserved.
- Same-NAT behavior: safe.
- Transfer behavior: handled by Velocity server-switch state, not by mc-router
  transfer intent.
- Operational complexity: high and changes the deployment model.
- Compatibility: one public UDP port can be preserved.
- Failure modes: more moving parts and a larger migration.
- Backend plugins required: official SVC proxy/backend support.
- Client changes required: no.

### 7. Separate public UDP ports per backend

- Correctness: simple and safe per backend.
- Security: avoids dynamic routing but exposes more public ports.
- Same-NAT behavior: safe.
- Transfer behavior: client must be told a backend-specific host/port after
  switch.
- Operational complexity: moderate for firewall/DNS/client documentation.
- Compatibility: does not preserve one shared public UDP port.
- Failure modes: port conflicts and client confusion.
- Backend plugins required: likely needed to advertise backend-specific ports.
- Client changes required: no mod changes, but operational endpoint changes.

## Decision

Transparent mc-router-only dynamic routing is rejected.

The simplest architecture that satisfies the required guarantees is:

```text
backend companion plugin/mod
  -> observes Simple Voice Chat setup for a player UUID
  -> registers UUID ownership for an allowed backend ID with mc-router

mc-router
  -> receives client-to-server UDP
  -> parses only the visible Simple Voice Chat UDP envelope
  -> looks up the clear UUID in authenticated registrations
  -> creates a bounded transport session to exactly one configured backend
  -> forwards backend replies only through that session
```

This PR adds the design record and a reusable parser for the visible UDP
envelope. It does not add route-level dynamic UDP configuration, a public
control plane, or dynamic routing behavior. A later implementation PR must add
the authenticated registration API, backend companion, dynamic relay, and real
client validation together.

## Proposed Registration Protocol

This protocol is not implemented in this PR.

- Bind address: internal-only by default, for example `127.0.0.1:9091` in local
  development or a cluster-internal Service in Kubernetes.
- Authentication: bearer token or HMAC token supplied through environment
  variables or Kubernetes Secrets, compared in constant time.
- Protocol version: explicit field, for example `version: 1`.
- Backend identity: a configured backend ID, not an arbitrary address.
- Registration key: Simple Voice Chat player UUID visible in client UDP.
- TTL: required and bounded.
- Operations: register/update, unregister, heartbeat.
- Reassignment: a register for the same UUID on a different allowed backend
  closes any stale UDP session for that UUID before accepting new traffic.
- Logging: never log tokens, raw voice secrets, raw UDP payloads, or arbitrary
  packet-derived strings.
- Metrics: fixed enum labels only.

## UDP Routing Requirements for the Future Implementation

- Parse only the visible client-to-server UUID and packet framing.
- Never decrypt or inspect Simple Voice Chat payloads.
- Drop unknown, expired, or ambiguous UUIDs.
- Do not route by source IP alone.
- Do not infer backend from `client IP + UDP source port`.
- Keep one backend-facing UDP socket per active client endpoint unless a later
  verified design proves a safer model.
- Bind backend replies to the transport session that created the backend socket.
- Close stale sessions when UUID ownership changes.
- Drop late replies from an old backend after reassignment.
- Preserve datagram boundaries and bytes.
- Bound sessions, registrations, logs, goroutines, and metrics labels.

## Transfer Lifecycle

Expected behavior for a safe implementation:

1. Hub registers UUID ownership for Hub after initial voice setup.
2. mc-router routes client UDP for that UUID to Hub.
3. Minecraft Transfer creates a new TCP connection with only target hostname
   visible to mc-router.
4. Old backend disconnect or new backend setup removes/replaces the old
   registration.
5. The new backend sends a new Simple Voice Chat secret packet through Minecraft
   plugin messaging.
6. The companion registers UUID ownership for the new backend.
7. mc-router closes stale UDP sessions for that UUID.
8. New UDP packets route only to the new backend.

Audio continuity is not claimed. A short voice reconnect is the expected safe
behavior unless real-client validation proves seamless continuity.

## Same-NAT Behavior

Same-NAT clients must remain distinct because:

- backend ownership is keyed by registered player UUID, not source IP;
- transport sessions are keyed by full client UDP endpoint after a UUID lookup;
- replies are returned only through the backend socket associated with that
  transport session.

Tests still required:

- two same-IP clients on one backend;
- two same-IP clients on different backends;
- one same-IP client transferring while another remains;
- reconnect with changed UDP source port;
- stale session cleanup from the same public IP.

## Kubernetes Design Direction

A production example should be added only after local dynamic routing works.
Expected shape:

- public Service exposes mc-router TCP `25565` and UDP `24454`;
- backend Simple Voice Chat UDP Services are cluster-internal only;
- registration API is cluster-internal only;
- NetworkPolicy allows registration only from backend namespaces or Pods;
- registration token is mounted from a Kubernetes Secret;
- backend IDs map to configured internal UDP Services;
- no backend UDP port is directly exposed publicly;
- rolling updates rely on registration TTLs and unregister events.

## Manual Validation Status

Not yet performed for dynamic routing:

- two real clients on Hub;
- bidirectional speaking/listening;
- Hub to C2ME transfer;
- C2ME to Hub transfer;
- Hub to Prison transfer;
- C2ME to Prison transfer;
- same-LAN behavior;
- backend restarts;
- mc-router restart;
- 30-minute sustained test;
- packet loss, latency, and jitter test;
- stale-session cleanup verification.

## Current Blocker

mc-router cannot safely implement dynamic Transfer-aware Simple Voice Chat
routing as a transparent TCP router alone. It lacks authenticated visibility into
the player's UUID/current backend relationship at the time Simple Voice Chat
creates or replaces its UDP session.

A safe implementation requires either:

- a backend companion that registers verified UUID-to-backend ownership with
  mc-router; or
- adopting an official proxy architecture such as Velocity/Bungee where the
  proxy participates in plugin messaging and owns current backend state.
