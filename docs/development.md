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
- Require approvals: `1` for a shared repo or team workflow; `0` is acceptable only for a solo repository where the maintainer self-reviews every PR.
- Require status checks before merging.
- Require branches to be up to date before merging.
- Require conversation resolution before merging.
- Block force pushes.
- Block branch deletion.
- Do not allow bypassing, if the repository plan supports it.
- Restrict who can push to matching branches: usually leave this off for `main` if pull requests plus status checks are enforced; enable it only if the repository has a small trusted maintainer set and you want an extra hard stop on direct pushes.

Recommended required checks:

- `gofmt` check
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- Docker build smoke

Do not mark the optional real-server E2E workflow as required at first.
If the repository has not run a workflow yet, GitHub may not offer the check names until the first successful run. In this repo, the relevant job-level checks are expected to appear as `ci / test` and `optional minecraft e2e / real minecraft server smoke`.

## GitHub UI Setup

If branch protection or rulesets are configured in the GitHub UI, use `main` as the target and keep the required checks limited to fast, deterministic validation. Add the optional real-server E2E later only if the workflow becomes stable enough and no longer depends on manual EULA acceptance.
## Troubleshooting: Codex CLI and GitHub CLI on Windows

When using GitHub CLI from Codex CLI on Windows, GitHub API commands need network access in the workspace sandbox:

```toml
[sandbox_workspace_write]
network_access = true
```

If `gh auth status` reports an invalid token while `api.github.com:443` is reachable, check whether `[windows] sandbox = "elevated"` is preventing `gh` from reading the same Windows keyring or Credential Manager entry as the normal user session. In this environment, switching temporarily to `[windows] sandbox = "unelevated"` allowed `gh auth status`, `gh api user`, and `gh pr create` to succeed, but treat that as an environment-specific workaround instead of a blanket recommendation.

Before GitHub API operations from Codex CLI, confirm:

```powershell
Test-NetConnection api.github.com -Port 443
gh auth status
gh api user
```

If any check fails, do not run `gh auth login`, `gh auth logout`, or `gh auth token` from Codex automatically. Switch to a normal PowerShell session, inspect GitHub CLI authentication there, and resume once `gh auth status` and `gh api user` succeed.
