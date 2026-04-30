# Release Notes

This repository follows the same PR-first rule for release-related work.

## Release Expectations

- Make release prep changes on a feature branch.
- Review release notes, workflow changes, and version bumps in a PR.
- Avoid mixing release changes with unrelated protocol or platform work.
- If a release task needs a direct fix on `main`, follow up with a PR that documents and corrects it.
- Tagging, release automation, and image publishing are not yet standardized in this repository. Treat those as future work until a dedicated release process is added.

## Manual E2E

The optional real Minecraft server E2E workflow remains manual through `workflow_dispatch`. Keep it out of the required release gate until it is stable and does not need explicit EULA acceptance in the normal release path.

## Future Container Image Publishing

Image publishing is not implemented yet. A future release workflow should build an OCI image in GitHub Actions and publish only from trusted repository events.

Candidate registries:

- GHCR: `ghcr.io/AlexanderGG-0520/mc-router`
- Docker Hub / DHCR: repository name is not decided yet

Candidate tag policy:

- `latest`: only for the stable `main` image after release criteria pass
- `vX.Y.Z`: Git tag release image
- `sha-<shortsha>`: traceable verification image
- `pr-<number>`: optional preview image if the project later needs it

Candidate architecture policy:

- `linux/amd64` first
- `linux/arm64` only after confirming operator demand and build/runtime coverage

Workflow policy:

- `pull_request`: build smoke only, no publish
- `main` merge: build and test, no public release tag by default
- Git tag push or release creation: publish versioned image
- GHCR publishing should use `GITHUB_TOKEN` where repository permissions allow it
- Docker Hub publishing requires `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` secrets

Security and supply chain notes:

- Never print registry credentials or tokens in logs.
- Do not publish images from pull requests opened from forks.
- Consider provenance, SBOM generation, and cosign signing after the basic release path is stable.

Kubernetes manifest policy:

- `deploy/kubernetes` examples should use a fixed image tag.
- Do not rely on `latest` for production examples.
- After a release, consider updating the manifest example image in a follow-up PR.
