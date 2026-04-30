# Development Workflow

## Day-to-Day Flow

1. Start from `main`.
2. Create a feature branch for the task.
3. Make the smallest useful change.
4. Run the pre-PR checks.
5. Open a pull request against `main`.
6. Wait for CI and review before merging.

## Required Local Checks

Run these before opening a PR:

```powershell
gofmt -l .
go test ./...
go test -race ./...
go vet ./...
go test -tags=e2e -run TestDoesNotExist ./test/e2e
docker build -t mc-gateway:dev .
```

## Pull Request Rules

- Do not push directly to `main`.
- Do not commit directly to `main`.
- Every change must come from a feature branch.
- Keep large work in smaller PRs when possible.
- Keep production proxy changes minimal and focused.
- Split Kubernetes discovery, wake-up, fallback, metrics, REST API, CRD, and Web UI work into separate PRs.
- The optional real Minecraft server E2E workflow is manual only and runs through `workflow_dispatch`.
- Leave the optional real-server E2E out of the required PR check set because it depends on explicit EULA acceptance.
- If `main` is updated directly by mistake, use a follow-up PR to correct it.

## GitHub Branch Protection / Ruleset

Target branch: `main`

Recommended settings:

- Require a pull request before merging.
- Require status checks before merging.
- Require branches to be up to date before merging.
- Require conversation resolution before merging.
- Block force pushes.
- Block branch deletion.
- Do not allow bypassing, if the repository plan supports it.

Recommended required checks:

- `gofmt` check
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- Docker build smoke

Do not mark the optional real-server E2E workflow as required at first.

## GitHub UI Setup

If branch protection or rulesets are configured in the GitHub UI, use `main` as the target and keep the required checks limited to fast, deterministic validation. Add the optional real-server E2E later only if the workflow becomes stable enough and no longer depends on manual EULA acceptance.

