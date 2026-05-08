# Release Notes

This repository follows the same PR-first rule for release-related work.

## Release Expectations

- Make release prep changes on a feature branch.
- Review release notes, workflow changes, and version bumps in a PR.
- Avoid mixing release changes with unrelated protocol or platform work.
- If a release task needs a direct fix on `main`, follow up with a PR that documents and corrects it.
- Tagging and GitHub Release creation remain manual for now. Container image publishing is handled by `.github/workflows/docker-publish.yml`.

## Manual E2E

The optional real Minecraft server E2E workflow remains manual through `workflow_dispatch`. Keep it out of the required release gate until it is stable and does not need explicit EULA acceptance in the normal release path.

## Container Image Publishing

The Docker publish workflow builds the repository `Dockerfile` and publishes only from trusted repository events. It does not run on pull requests.

Publish targets:

- GHCR: `ghcr.io/alexandergg-0520/mc-router`
- Docker Hub official image: `alecjp02/mc-router`
- Repository variable: `DOCKERHUB_IMAGE=alecjp02/mc-router`
- Manual workflow input: `dockerhub_image` is for temporary override only; normal operation uses `DOCKERHUB_IMAGE`
- README Docker Pulls / Docker Stars badges intentionally point to `alecjp02/mc-router`; that repository is the official Docker Hub image

Tag policy:

- branch pushes publish the branch tag, such as `main`
- Git tag pushes publish the tag ref, such as `v0.1.0-alpha`
- Semver tags also publish `{{version}}`; stable tags without a prerelease suffix also publish `{{major}}.{{minor}}`
- `sha-<shortsha>` is included as a traceable image tag
- `latest` is intentionally not published for `v0.1.0-alpha`

Architecture policy:

- The publish workflow builds `linux/amd64` and `linux/arm64`.
- The Dockerfile uses a static Go binary and a distroless runtime image, which are suitable for multi-arch buildx publishing.

Workflow policy:

- `pull_request`: build smoke only, no publish
- `main` merge: publish the `main` and `sha-<shortsha>` image tags
- Git tag push: publish versioned image tags
- `workflow_dispatch`: publish manually from the selected ref
- GHCR publishing should use `GITHUB_TOKEN` where repository permissions allow it
- Docker Hub publishing requires all of:
  - `DOCKERHUB_USERNAME` secret
  - `DOCKERHUB_TOKEN` secret
  - `DOCKERHUB_IMAGE` repository variable, with `dockerhub_image` as an optional manual override
- If Docker Hub configuration is incomplete, the workflow still publishes GHCR and skips Docker Hub.

Security and supply chain notes:

- Never print registry credentials or tokens in logs.
- Do not publish images from pull requests opened from forks.
- Consider provenance, SBOM generation, and cosign signing after the basic release path is stable.

Kubernetes manifest policy:

- `deploy/kubernetes` examples should use a fixed image tag.
- Do not rely on `latest` for production examples.
- After a release, consider updating the manifest example image in a follow-up PR.
