# Releasing

Releases are driven by **version tags**. Pushing a tag like `v1.2.3` triggers the [Release workflow](../.github/workflows/release.yml). That workflow creates a draft GitHub release, builds the Docker image for `linux/amd64` and `linux/arm64` and pushes it to `ghcr.io/mattoddie/unifi-mcp`. It then builds the MCP Bundles (`.mcpb`) for Claude Desktop and attaches them to the draft, publishes the release, and publishes stable releases to the [MCP Registry](https://registry.modelcontextprotocol.io) as `io.github.mattoddie/unifi-mcp`. Nothing is published by merging to `main` alone.

- [Versioning](#versioning)
- [Release checklist](#release-checklist)
- [Pre-releases](#pre-releases)
- [What the workflow does](#what-the-workflow-does)
- [If something goes wrong](#if-something-goes-wrong)
- [Repository setup](#repository-setup)

## Versioning

The project follows [Semantic Versioning](https://semver.org). Its public API is the set of **tool names, their arguments and the environment variables**:

| Change | Bump | Examples |
|---|---|---|
| Breaking | **major** (`2.0.0`) | Renaming or removing a tool, argument or variable; changing a default so that more is allowed |
| New features | **minor** (`1.3.0`) | New tools, toolsets, arguments or prompts |
| Fixes | **patch** (`1.2.4`) | Bug fixes, compatibility fixes, docs, dependency updates |

While the version is `0.x`, minor bumps may contain breaking changes. Call them out in the release notes.

The version comes **from the tag**. The code says `0.0.0-dev` in the repository, and release builds stamp the tag's version into the binary (`-ldflags`, through the `VERSION` build argument) and into each bundle's `manifest.json`. The server reports it to MCP clients and prints it on its first log line.

## Release checklist

1. **Check `main` is green** on [CI](https://github.com/mattoddie/unifi-mcp/actions/workflows/ci.yml).
2. **Update the changelog.** In a small PR titled `chore(release): v1.2.0`, move the **Unreleased** entries in [`CHANGELOG.md`](../CHANGELOG.md) under a new heading, `## [1.2.0] - YYYY-MM-DD`, and update the comparison links at the bottom. Merge it.
3. **Push the tag.** Tag the changelog commit on `main`:

   ```sh
   git switch main && git pull
   git tag -s v1.2.0 -m v1.2.0
   git push origin v1.2.0
   ```

4. **Watch the workflow.** Open **Actions → Release**. It takes a few minutes. It creates a draft release with notes generated from the merged PRs, adds a *Container image* section with the pull command, digest and tags, attaches the bundles and adds a *Claude Desktop* section listing them, then publishes the release.
5. **Edit the release notes** if needed. The notes and title of a published release can still be edited; its assets and tag can't.
6. **Check the MCP Registry** (stable releases only): `curl -s https://registry.modelcontextprotocol.io/v0.1/servers/io.github.mattoddie%2Funifi-mcp/versions/latest | jq .server.version` prints the new version.

Release notes are generated from merged PR titles and grouped by label. The groups are set in [`.github/release.yml`](../.github/release.yml). Label PRs `breaking`, `enhancement`, `new-tool`, `bug`, `compatibility`, `documentation` or `dependencies` to sort them, or `skip-changelog` to leave a PR out.

## Pre-releases

To test a release candidate, push a pre-release tag. The workflow marks a release as a pre-release when its tag has a suffix:

```sh
git tag -s v1.3.0-rc.1 -m v1.3.0-rc.1
git push origin v1.3.0-rc.1
```

Pre-releases are published only under their exact version (`ghcr.io/mattoddie/unifi-mcp:1.3.0-rc.1`). They never move `latest`, `1` or `1.3`. They aren't published to the MCP Registry, which would list them as the latest version.

## What the workflow does

| Step | Detail |
|---|---|
| Validate the tag | Must match `vMAJOR.MINOR.PATCH` with an optional `-prerelease` suffix and point to a commit on `main`, otherwise the workflow fails before building. |
| Draft release | Creates a draft release for the tag with generated notes, marked as a pre-release if the tag has a suffix. |
| Build | `docker buildx` for `linux/amd64` and `linux/arm64`. The test suite runs inside the build, so failing tests mean no image. |
| Tag | `X.Y.Z` always. For stable releases also `X.Y`, `X` (from `1.0.0` on) and `latest`. |
| Push | To `ghcr.io/mattoddie/unifi-mcp` using the workflow's `GITHUB_TOKEN`. No personal tokens are needed. |
| Supply chain | Attaches an SBOM and SLSA provenance to the image, and pushes a signed build provenance attestation (`gh attestation verify`). |
| Release notes | Appends the pull command, digest and tags. |
| MCP Bundles | After the image, runs `make mcpb VERSION=X.Y.Z`: one `unifi-mcp-X.Y.Z-<os>-<arch>.mcpb` for each of `darwin`, `windows` and `linux` on `amd64` and `arm64`. It validates each manifest with the official `@anthropic-ai/mcpb` CLI (pinned in the workflow), attests their build provenance, uploads them to the release and lists them in the notes. |
| Publish | Publishes the draft once the image and the bundles are in place. Published releases are [immutable](#immutable-releases). |
| MCP Registry | Stable releases only, after the release is published. Stamps the version, the image tag and each bundle's URL and SHA-256 into [`server.json`](../server.json), validates it, logs in with GitHub OIDC and publishes it with `mcp-publisher` (pinned, checksum verified). |

### About the bundles

An [MCP Bundle](https://github.com/modelcontextprotocol/mcpb) is a zip file with a `manifest.json`, the icon, the licence and the server binary. The manifest lives in [`mcpb/manifest.json`](../mcpb/manifest.json): edit its descriptions and settings by hand, while `make docs` regenerates its `tools` and `prompts` from the server (CI checks they're current). [`cmd/mcpb`](../cmd/mcpb/main.go) stamps the version and platform into a copy and writes the zip.

There's one bundle per OS and architecture because a manifest can only choose the command by OS, not by CPU. Try one locally with `make mcpb` (the bundles land in `dist/`).

The bundles aren't signed. The macOS binaries carry the Go linker's ad-hoc signature, which Apple silicon requires, but they aren't signed with a Developer ID or notarised, so macOS may ask the user to allow them (see [Claude Desktop](clients.md#if-it-doesnt-start)). `mcpb sign` can add a signature to the bundle itself, but that needs a code-signing certificate and isn't set up.

### About the MCP Registry

The [MCP Registry](https://registry.modelcontextprotocol.io) lists unifi-mcp as `io.github.mattoddie/unifi-mcp`, so MCP clients and directories can find and install it. Its entry comes from [`server.json`](../server.json), which describes two kinds of package:

- **The image** (`ghcr.io/mattoddie/unifi-mcp:X.Y.Z`) over stdio. The image serves HTTP by default, so the package sets `MCP_TRANSPORT=stdio` as a fixed value. It declares the same settings as the bundles, except the CA file: a file on the user's computer isn't visible inside the container, so use the fingerprint instead.
- **The six bundles**, each with its SHA-256. The registry's format can't say which OS or CPU a package is for, so the file names carry it, as other servers on the registry do.

The committed file says `0.0.0-dev`, like the code. The release job stamps the real version, the image tag and the bundles' URLs and SHA-256s into a copy (`make server-json VERSION=X.Y.Z`, which writes `dist/server.json` from the bundles in `dist/`) and publishes that.

The registry checks that the packages belong to the server: the image must carry the `io.modelcontextprotocol.server.name` label (set in the [`Dockerfile`](../Dockerfile)), and the bundles must be GitHub release downloads. The job logs in with the workflow's OIDC token, which the registry accepts for repositories in the `mattoddie` organisation. No secret is needed.

A published version can't be changed or published again. To fix the listing, release a new version.

Edit `server.json` by hand when a setting changes. CI validates it with `mcp-publisher validate` on every pull request, and `go test` fails if its variables (names, required, secret and default values) don't match the bundle's [`manifest.json`](../mcpb/manifest.json).

`mcp-publisher` is pinned in [`release.yml`](../.github/workflows/release.yml) and [`ci.yml`](../.github/workflows/ci.yml) by version and SHA-256, and Dependabot doesn't update it. To upgrade, change `MCP_PUBLISHER_VERSION` and `MCP_PUBLISHER_SHA256` in both (the SHA-256 of `mcp-publisher_linux_amd64.tar.gz` is listed in the release's `registry_X.Y.Z_checksums.txt`).

## If something goes wrong

- **The workflow failed** for a transient reason: re-run it from the Actions tab. It's safe to re-run. It finds the draft it created, and pushes the same image tags again. If the release was already published, its bundles can't change, so a re-run rebuilds the image only.
- **The MCP Registry job failed:** re-run just that job (**Re-run failed jobs**). If the version was already published, it says so and stops. If it keeps failing, for example because the registry is down, publish by hand once the release's bundles are attached:

  ```sh
  git checkout v1.2.0
  gh release download v1.2.0 --pattern '*.mcpb' --dir dist
  make server-json VERSION=1.2.0
  mcp-publisher validate dist/server.json
  mcp-publisher login github      # in a browser, as an owner of the mattoddie organisation
  mcp-publisher publish dist/server.json
  ```

  Install the same `mcp-publisher` version the workflow pins, from its [releases](https://github.com/modelcontextprotocol/registry/releases).
- **The release was wrong** (bad code or wrong tag): a published tag can't be moved or reused. Fix the problem on `main` and publish the next patch version. You can delete the bad GitHub release and mark it as such in the changelog, but leave the image tag alone.
- **The workflow failed before publishing** and the tag itself is wrong: delete the draft and the tag (`gh release delete v1.2.0 --cleanup-tag`), then tag again.
- **Wrong tag format** (e.g. `1.2.0` without the `v`): the workflow doesn't run for it. Delete the tag (`git push origin :refs/tags/1.2.0`) and push the right one.

## Repository setup

### Rulesets (in place)

Two rulesets protect the repository. You can view and edit them under **Settings → Rules → Rulesets**.

**Protect main** (the default branch):

- Changes land only through pull requests. Nobody can push directly, force-push or delete the branch, and there are no bypasses.
- The CI checks *Test*, *Docker image* and *Dependency review* must pass. The branch doesn't need to be up to date with `main`.
- CodeQL must not find new high or critical security alerts, or error-level alerts.
- Review threads must be resolved before merging. The only merge method is squash.
- No approving review is required yet, because GitHub doesn't let authors approve their own pull requests and the project has a single maintainer. Raise this when there are more maintainers.

**Protect release tags** (`v*`):

- Only repository admins can create, move or delete version tags, so only maintainers can publish releases. Published versions can't be re-pointed.

### Immutable releases

[Immutable releases](https://docs.github.com/en/code-security/supply-chain-security/understanding-your-software-supply-chain/immutable-releases) are on (**Settings → General → Releases**). Once a release is published, its assets can't be added, changed or deleted, and its tag can't be moved or reused, even by admins. GitHub also attaches a release attestation (`gh release verify`). That's why the workflow attaches everything to a draft and publishes last.

> [!NOTE]
> Required checks are matched by job name. If you rename a job in [`ci.yml`](../.github/workflows/ci.yml), update the *Protect main* ruleset in the same change. Otherwise pull requests wait forever for a check that never reports.

### Container package visibility

New GHCR packages normally start private, and GitHub doesn't offer an API to change this. This package became public automatically because it's linked to this public repository. If a new package ever starts private, open the package page (**Your profile → Packages → unifi-mcp → Package settings**) and choose **Change visibility → Public** under **Danger Zone**.

The package links to the repository automatically through the image's `org.opencontainers.image.source` label, which makes it show in the repository sidebar.
