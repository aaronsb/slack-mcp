# Release Runbook

## Typical Release

```bash
# 1. Ensure main is clean
git checkout main && git pull && make build && make test

# 2. Tag the release
make release TAG=vX.Y.Z
```

This creates an annotated tag and pushes it. Two GitHub Actions fire:
- **npm-publish**: cross-compiles, publishes 7 npm packages with provenance
- **release**: creates GitHub Release with binaries + checksums

## Verify

```bash
# Watch CI
gh run list --limit 3
gh run watch <run-id>

# Check npm
npm view @aaronsb/slack-mcp version

# Check GitHub Release
gh release view vX.Y.Z
```

## First Publish (one-time)

Scoped packages need `--access public` on first publish. The Makefile and CI
handle this, but the first publish requires npm login + OTP:

```bash
npm login
make build-all-platforms
make npm-copy-binaries
make npm-set-version NPM_VERSION=1.0.0
make npm-publish NPM_PUBLISH_FLAGS="--otp=XXXXXX"
```

After first publish, set up the `NPM_TOKEN` secret in GitHub repo settings
for CI-based publishing.

## Pre-release

```bash
make release TAG=v1.1.0-alpha.1
```

CI auto-detects the pre-release tag and publishes with `--tag alpha` instead
of `--tag latest`.

## Retagging (if CI fails)

```bash
git tag -d vX.Y.Z
git push origin :refs/tags/vX.Y.Z
# Fix the issue, commit, push
make release TAG=vX.Y.Z
```

## Manual Recovery

If `make release` fails partway:

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```
