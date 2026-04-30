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
