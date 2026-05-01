# AGENTS.md

## Project overview

mc-router is a lightweight Minecraft server router/proxy helper intended to run in containerized environments such as Docker and Kubernetes.

The project prioritizes:

- predictable behavior in container environments
- small and reviewable changes
- clear documentation
- safe defaults
- CI-backed validation

## Development rules

- Keep changes small and focused.
- Do not mix unrelated refactors into feature or documentation changes.
- Preserve existing behavior unless the task explicitly asks to change it.
- Prefer simple, readable Go code over clever abstractions.
- Avoid adding new dependencies unless there is a clear benefit.
- Do not introduce OS-specific assumptions unless they are documented and necessary.
- This project is primarily expected to run in Docker or Kubernetes.
- Do not commit or push directly to `main`; use a feature branch and pull request.
- Keep the required `test` check intact. Optional Minecraft E2E workflows are useful but are not required checks.

## Safety rules

- Do not make destructive changes without an explicit reason.
- Do not remove files, workflows, tests, or documentation unless the task requires it.
- Do not weaken CI, lint, smoke tests, or security checks to make a change pass.
- Do not hard-code secrets, tokens, private registry credentials, IP addresses, or environment-specific values.
- Do not print token values or secret material in logs, PR text, or agent responses.
- Do not change public behavior without updating docs.
- Do not run `gh auth login`, `gh auth logout`, or `gh auth token` unless the user explicitly asks.

## Validation

Before finishing a code change, run the relevant checks when possible:

```text
gofmt -l .
go test ./...
go test -race ./...
go vet ./...
```

If Docker-related files are changed, also inspect build/runtime assumptions.

If GitHub Actions workflows are changed, verify that the workflow still matches the repository purpose and does not grant excessive permissions.

## Documentation rules

- Update README.md or docs/ when behavior, configuration, CLI usage, environment variables, or deployment assumptions change.
- Documentation should be understandable to users running the project in Docker or Kubernetes.
- Avoid unnecessary OS support claims. If the software runs inside containers, document the container/runtime assumptions instead.

## Pull request rules

A PR should explain:

- what changed
- why it changed
- how it was validated
- whether the change affects runtime behavior
- whether docs were updated

Keep PRs narrow enough to review comfortably.

Repository rules are expected to require pull requests, keep branches up to date, block force pushes and branch deletion, and require conversation resolution. Do not weaken branch protection, CI, lint, smoke tests, or security checks to make a change pass.

## Agent behavior

When working as an AI coding agent:

- First inspect the current repository state.
- Check the current branch, short status, staged diff, GitHub auth status, and main branch freshness before making changes.
- Identify the smallest safe change that satisfies the task.
- Prefer patches that are easy for a human to review.
- Explain any risky assumption.
- If tests or CI cannot be run locally, say so clearly.
- Do not claim validation was performed unless it actually was.
- Treat Windows and PowerShell as the default local shell unless the user says otherwise.
- Avoid assuming `sh -lc`, `awk`, `sed`, or Unix-only one-liners are available.
- Be careful with shell commands that could disrupt the user's terminal, SSH session, credentials, or environment.
- Respect sandbox and network limits. If a necessary command is blocked, ask for approval instead of trying to bypass the restriction.
