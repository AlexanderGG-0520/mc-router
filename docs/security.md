# Security

## MVP Protections

- Handshake packet length is capped.
- VarInt decoding rejects values longer than 5 bytes.
- Server address length is capped.
- Empty server addresses are rejected.
- Unsupported next states are rejected.
- Trailing bytes in the handshake packet are rejected.
- Handshake read timeout limits slow scanners.
- Backend dial timeout limits stuck outbound connections.
- Optional client IP allow/deny CIDR policy rejects disallowed connections before parsing a Minecraft handshake.
- Optional per-IP token-bucket limits reject excessive connection attempts before parsing a Minecraft handshake. State is bounded and entries expire after inactivity.
- Unknown hosts are denied by default.
- Structured logs avoid raw packet payloads.
- Runtime container runs as non-root.
- Kubernetes example drops Linux capabilities and uses a read-only root filesystem.

## Logging

The gateway logs:

- remote socket address
- normalized requested server address
- backend selected
- route match type
- high-level error reason

The gateway does not log:

- raw handshake payload
- complete TCP stream contents
- authentication tokens
- player chat or commands

Remote IP addresses can be personal data in some environments. Retention and access policy should be handled outside the MVP.

## Deny Behavior

The default `unknownHostPolicy` is `deny`. This is safer for a public Minecraft entry point because random scanners and mistyped hosts do not reach a backend unless the operator explicitly opts into a default route.

## Bedrock Host Proxy

`bedrock.mode: host-proxy` terminates the Bedrock login in `mc-router` so it can read the requested `ServerAddress` and select a backend. This is different from opaque UDP forwarding. The backend sees a new Bedrock connection originated by `mc-router`, so Geyser/Floodgate authentication and identity handling must be validated for the deployment before production use.

## Known Gaps

- No PROXY protocol support.
- No Minecraft status response for denied hosts.
- No dynamic reload.
- No Kubernetes NetworkPolicy sample yet.
- No wake-up timeout policy.

These should be added as explicit gateway policies, not hidden inside backend server images.
