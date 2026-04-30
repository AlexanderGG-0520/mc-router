# Contributing

This repository uses a pull-request-first workflow.

## Branching

- Never push directly to `main`.
- Never commit directly to `main`.
- Create a feature branch for every change.
- Keep large platform changes separate from protocol, docs, and operational changes.

Recommended branch names:

- `chore/require-pr-workflow`
- `docs/development`
- `feat/minecraft-e2e`
- `fix/proxy-status-handshake`

## Before Opening a PR

Run these commands from your feature branch:

```powershell
gofmt -l .
go test ./...
go test -race ./...
go vet ./...
go test -tags=e2e -run TestDoesNotExist ./test/e2e
docker build -t mc-gateway:dev .
```

The optional real Minecraft server E2E workflow is triggered manually with `workflow_dispatch`. It requires explicit EULA acceptance, so it should stay out of the normal required PR checks.

## Change Scope

- Keep production proxy changes as small as possible.
- Split Kubernetes discovery, wake-up control, fallback behavior, metrics, REST API, CRDs, and Web UI into separate PRs.
- Do not pack unrelated large changes into a single PR.
- If `main` is updated directly by mistake, fix it in a follow-up PR rather than layering more direct pushes on top.

See [docs/development.md](docs/development.md) for branch protection and release-flow recommendations.
