# Release Runbook

## Typical Release

```bash
git checkout main && git pull
make release-patch    # or release-minor / release-major
```

The version is computed from what the repo already records, so there is nothing
to work out or type. `make release` runs `check` first, so tests, vet and
formatting gate the tag; it then writes the version into every source, commits
that, tags, and pushes.

Use `make release TAG=vX.Y.Z` when the number is not simply the next one — a
pre-release, or a major you want to name explicitly.

**Tags are signed, so this needs a terminal.** gpg opens a pinentry dialog
during `git tag`; it cannot run unattended.

Pushing the tag is the only outward step. Two GitHub Actions fire:
- **npm-publish**: cross-compiles, publishes 7 npm packages with provenance by OIDC, then the MCP Registry
- **release**: creates GitHub Release with binaries + checksums

## Verify

```bash
# Watch CI
gh run list --limit 3
gh run watch <run-id>

# Check npm
npm view @aaronsb/slack-mcp version

# Provenance — a token publish produces none, so this is the proof OIDC ran
npm view @aaronsb/slack-mcp dist.attestations

# MCP Registry (its search index lags its writes by about a minute;
# trust the publish log over this)
curl -s "https://registry.modelcontextprotocol.io/v0/servers?search=io.github.aaronsb/slack-mcp" | jq '.servers[].version' 

# Check GitHub Release
gh release view vX.Y.Z
```

## Authentication

There is none to manage. Publishing authenticates by GitHub OIDC — no
`NPM_TOKEN`, no stored credential, nothing to expire. npm matches the workflow
against the trusted publisher registered for each package.

**The workflow filename is load-bearing.** All seven packages are registered
against `npm-publish.yml`; renaming that file breaks authentication with nothing
in the repo to explain why.

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
