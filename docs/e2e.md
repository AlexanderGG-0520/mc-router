# Optional Real Minecraft Server E2E

## Purpose

The normal CI suite uses unit tests, fake TCP backends, and lightweight Minecraft protocol smoke tests. Those tests are fast and deterministic, and they verify that `mc-gateway` preserves the original handshake bytes while routing and proxying status/login traffic.

The optional real-server E2E smoke test adds one more check: a real Minecraft Java Edition server accepts traffic through the gateway and answers the first protocol flows that real clients use.

This is validation infrastructure only. It does not add production gateway behavior.

## Chosen Approach

The first implementation uses the `itzg/minecraft-server:java21` Docker image with `TYPE=PAPER` and a pinned Minecraft version.

Why this was chosen:

- It is easy to run in GitHub Actions and locally with Docker.
- The image already handles Java runtime setup, Paper download, server launch, and logs.
- Paper starts faster and is easier to automate than a manually managed vanilla server jar.
- The workflow can publish the container port on `127.0.0.1` with a random host port to avoid collisions.

Alternatives considered:

- Paper server directly on GitHub Actions: good startup behavior and real server compatibility, but we would need to script Java setup, Paper download URL handling, cache policy, logs, and EULA handling ourselves.
- Vanilla server directly on GitHub Actions: closest to Mojang's reference server, but startup and jar download handling are more brittle, and automation still needs explicit EULA handling.
- `itzg/minecraft-server` Docker image: selected because it keeps server setup repeatable while leaving the EULA acceptance explicit.
- Self-managed lightweight server jar: not selected because an unofficial or custom jar would weaken the value of a "real Minecraft server" test and could create unclear licensing/supply-chain questions.

## EULA And External Dependency Notes

Starting a Minecraft server requires accepting the Minecraft EULA. In the optional workflow, `EULA=TRUE` is passed to the container only after the manual `accept_minecraft_eula` input is set to `true`. Setting `EULA=TRUE` means the person running the workflow is asserting that they accept the EULA for that run.

The Docker image is pulled from Docker Hub as `itzg/minecraft-server:java21`. With `TYPE=PAPER`, the container downloads a Paper server build for the configured `VERSION`. Paper is an external server distribution, and its download happens at runtime inside the container. The workflow pins the Minecraft version input by default to `1.21.1` and uses protocol version `767`.

This optional workflow is not triggered by `pull_request` and is not a required status check.

## What The Test Covers

The E2E test can start the gateway in two modes. In GitHub Actions, the workflow builds the real `mc-gateway` binary, writes a temporary route config, and starts that binary. If `MC_ROUTER_E2E_GATEWAY_BIN` is not set, the test starts the same proxy/router/config path in-process for simpler local development.

In both modes, the route config maps `e2e.example.com` to the real server backend.

The status flow verifies:

- TCP connection to `mc-gateway`.
- Handshake with `next_state=status`.
- Status request through the gateway.
- JSON status response from the real server.
- Ping request through the gateway.
- Pong response with the exact payload echoed by the real server.

The login start flow verifies:

- Handshake with `next_state=login`.
- Login Start packet through the gateway.
- A real login-state response from the server.
- In the GitHub Actions workflow, the server runs with `ONLINE_MODE=TRUE`, but the first optional E2E keeps login validation intentionally loose and accepts any known login-state response. This keeps status flow as the hard real-server guarantee while still proving that login-start bytes reach a real server through the gateway.

The test does not complete encryption, compression negotiation, login success, configuration state, or play state.

## What This Still Does Not Guarantee

This smoke test does not guarantee:

- Full Minecraft client compatibility.
- Login completion.
- Encrypted stream proxying after the client responds to the encryption request.
- Compression handling after login.
- Play state packet behavior.
- Modded protocol compatibility.
- Load, soak, or capacity behavior.
- Kubernetes discovery, wake-up, fallback, metrics, REST API, CRDs, Web UI, or hot reload behavior.

## Flake Controls

The workflow and test avoid common CI flakes by:

- Publishing the Minecraft container on a random `127.0.0.1` host port.
- Starting the gateway on a random local port.
- Retrying the status and login flows with a deadline instead of using a fixed startup sleep.
- Setting per-connection deadlines in the smoke client.
- Setting `go test -timeout`.
- Setting a workflow job `timeout-minutes`.
- Printing Minecraft server logs on failure.
- Printing `mc-gateway` logs from the E2E test when the test fails.

The workflow does not currently depend on a Docker container health check. The real readiness signal for this project is whether a Minecraft status request and ping round trip through `mc-gateway`; the E2E client retries that protocol flow until the deadline.

## Local Run With PowerShell

These commands start the same kind of Paper server used by the optional workflow. Only run them if you accept the Minecraft EULA.

```powershell
docker run --detach --name mc-router-e2e-minecraft --publish 127.0.0.1::25565 --env EULA=TRUE --env TYPE=PAPER --env VERSION=1.21.1 --env ONLINE_MODE=TRUE --env ENABLE_RCON=false --env MEMORY=1G --env MOTD="mc-router e2e" itzg/minecraft-server:java21
```

Get the random published port and run the E2E test:

```powershell
$published = docker port mc-router-e2e-minecraft 25565/tcp | Select-Object -First 1
$port = ($published -split ":")[-1]
$env:MC_ROUTER_E2E_BACKEND = "127.0.0.1:$port"
$env:MC_ROUTER_E2E_PROTOCOL_VERSION = "767"
$env:MC_ROUTER_E2E_TIMEOUT = "8m"
$env:MC_ROUTER_E2E_LOGIN_EXPECT = "any"
go test -v -tags=e2e -timeout=15m ./test/e2e
```

To run the smoke test against the actual `mc-gateway` binary locally, build it first and set `MC_ROUTER_E2E_GATEWAY_BIN`:

```powershell
go build -o .\mc-gateway.exe .\cmd\mc-gateway
$env:MC_ROUTER_E2E_GATEWAY_BIN = "$PWD\mc-gateway.exe"
go test -v -tags=e2e -timeout=8m ./test/e2e
```

Show logs and clean up:

```powershell
docker logs mc-router-e2e-minecraft
docker rm -f mc-router-e2e-minecraft
```

For an offline-mode local server, set `MC_ROUTER_E2E_LOGIN_EXPECT=any` because the first login response may be Set Compression or Login Success instead of Encryption Request.

Supported login expectations:

- `any`: accept any known login-state response packet. This is the default and the workflow setting.
- `encryption_request`: require packet `0x01` and validate the basic encryption request fields. Use this when testing an online-mode server/version known to answer with encryption first.
- `skip`: skip the login-start smoke test and only require the status flow.

## GitHub Actions Run

Use the manual workflow named `optional minecraft e2e`.

1. Open the Actions tab.
2. Select `optional minecraft e2e`.
3. Choose `Run workflow`.
4. Set `accept_minecraft_eula` to `true` only if you accept the Minecraft EULA for the run.
5. Keep the defaults for Minecraft `1.21.1` and protocol `767`, or change both together for another server version.

The workflow builds `mc-gateway`, starts Paper through Docker, waits by retrying real protocol flows, runs `go test -tags=e2e ./test/e2e`, prints logs, and removes the container.

With GitHub CLI, first confirm that you are authenticated and that you accept the Minecraft EULA for the run:

```powershell
gh auth status
gh workflow run e2e-minecraft.yml -f accept_minecraft_eula=true -f minecraft_version=1.21.1 -f protocol_version=767
```

Then find and watch the run:

```powershell
gh run list --workflow e2e-minecraft.yml --limit 1
gh run watch <run-id>
gh run view <run-id> --log
```

## Verified Manual Run

Manual run `25160991609` verified the optional real Minecraft server E2E workflow on 2026-04-30 UTC:

- Run URL: <https://github.com/AlexanderGG-0520/mc-router/actions/runs/25160991609>
- Workflow: `optional minecraft e2e`
- Branch: `main`
- Head SHA: `d45851e67102edac46f56783f03720628caf2c6d`
- Minecraft version: `1.21.1`
- Protocol version: `767`
- Server: `itzg/minecraft-server:java21` with `TYPE=PAPER`
- Result: success
- Status flow: `TestRealMinecraftStatusThroughGateway` passed
- Login start flow: `TestRealMinecraftLoginStartThroughGateway` passed
- Login response: encryption request, packet `0x01`, payload `171` bytes

This remains an optional manual workflow, not a required PR gate. Manual execution requires explicit Minecraft EULA acceptance. The GitHub Actions logs showed a Node.js 20 deprecation warning for upstream actions, but it did not affect this run.

## Troubleshooting

If the workflow fails before starting Docker, check the `Confirm EULA acceptance` step. `accept_minecraft_eula=false` is expected to fail before `EULA=TRUE` is ever passed to the container.

If the `Start Paper server` step fails, check whether Docker Hub or the Paper download endpoint was temporarily unavailable. The workflow depends on pulling `itzg/minecraft-server:java21` from Docker Hub and on the container downloading Paper for the selected `VERSION`.

If `Docker did not publish the Minecraft server port` appears, inspect the `Start Paper server` output. The workflow uses Docker's random host port mapping with `--publish 127.0.0.1::25565`; the E2E test receives the parsed `127.0.0.1:<port>` through `MC_ROUTER_E2E_BACKEND`.

If the status flow times out, read the `Minecraft server logs` step first. Common causes are slow Paper download/startup, version download failure, EULA input left false, or a container crash. The test error includes the number of protocol attempts and the last connection/protocol error.

If the login-start flow fails but status succeeds, the gateway has already proven the primary real-server status path. Check whether the selected Minecraft version changed the Login Start packet or the first clientbound login response. For a first-pass workflow run, set `MC_ROUTER_E2E_LOGIN_EXPECT=any` or `skip`; use `encryption_request` only after confirming the version's login packet shape.

If `protocol_version` or `minecraft_version` is changed, update both together. Minecraft `1.21.1` uses protocol `767`. Status may tolerate some protocol mismatches, but login-start packets are more sensitive to version-specific fields.
