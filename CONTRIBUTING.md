# Contributing to unifi-mcp

Thanks for wanting to help! unifi-mcp is a community project, and every kind of contribution is welcome: bug reports, compatibility reports from your hardware, documentation fixes, new tools and code review.

By taking part you agree to follow the [code of conduct](CODE_OF_CONDUCT.md).

- [Ways to contribute](#ways-to-contribute)
- [Development setup](#development-setup)
- [Making a change](#making-a-change)
- [Pull request guidelines](#pull-request-guidelines)
- [Commit messages](#commit-messages)
- [Adding or changing tools](#adding-or-changing-tools)
- [Releases](#releases)
- [Getting help](#getting-help)

## Ways to contribute

- **Report a bug.** Use the [bug report form](https://github.com/mattoddie/unifi-mcp/issues/new?template=bug_report.yml). Include your controller model, Network version and the exact error, and strip out credentials and public IPs.
- **Report compatibility.** UniFi's APIs differ between versions and hardware, and nobody owns every device. A [compatibility report](https://github.com/mattoddie/unifi-mcp/issues/new?template=compatibility.yml) saying *"works on my UCG Max with Network 9.4"* is genuinely useful.
- **Suggest a feature.** Open a [feature request](https://github.com/mattoddie/unifi-mcp/issues/new?template=feature_request.yml). If you can find the API call the UniFi web UI makes (browser dev tools → Network tab), include it. It's often most of the work.
- **Improve the docs.** Typos, unclear steps and missing examples are all fair game. Small docs fixes can go straight to a pull request.
- **Write code.** Look for issues labelled [`good first issue`](https://github.com/mattoddie/unifi-mcp/labels/good%20first%20issue) or [`help wanted`](https://github.com/mattoddie/unifi-mcp/labels/help%20wanted). For anything big, please open an issue first so we can agree on the approach before you invest time.
- **Report a vulnerability privately.** See [SECURITY.md](SECURITY.md). Please don't open a public issue for it.

## Development setup

You need **Go 1.25+** and, to build the image, **Docker**.

```sh
git clone https://github.com/<you>/unifi-mcp.git   # your fork
cd unifi-mcp
make test
```

`make test` runs the whole suite against a built-in mock controller in a few seconds. You don't need a real UniFi controller to contribute.

See [docs/development.md](docs/development.md) for the architecture, the mock controller and a walkthrough of adding a tool.

## Making a change

1. **Fork** the repository and create a branch from `main`, for example `git checkout -b feat/wireguard-peers`.
2. **Make your change** with tests. Keep it focused: one feature or fix per pull request.
3. **Run the checks** that CI runs:

   ```sh
   make lint            # gofmt and go vet
   make test            # tests (CI also runs them with -race)
   make docs-check      # docs/tools.md and mcpb/manifest.json match the code
   docker build .       # optional: the image builds and the tests pass inside it
   ```

4. **Update the docs.** If you changed tools or prompts, run `make docs`. Update `README.md` or `docs/` if behaviour changed, and add a line under **Unreleased** in [CHANGELOG.md](CHANGELOG.md) for anything users will notice.
5. **Open a pull request** against `main` with a [Conventional Commits](#commit-messages) title, and fill in the template.

## Pull request guidelines

- CI must be green. It checks formatting and `go vet`, runs the tests with the race detector, checks the generated docs, builds the image for amd64 and arm64, and smoke-tests the container.
- Match the existing style: `gofmt`, small focused functions, and comments that explain *why* rather than *what*.
- **No new dependencies without discussion.** The project deliberately stays small. The only direct dependency is the official MCP Go SDK, `github.com/modelcontextprotocol/go-sdk`.
- Never include real credentials, API keys, MAC addresses, public IPs or other personal network data in code, tests, fixtures or screenshots. Use obviously fake values like `aa:bb:cc:00:00:01` and `192.0.2.1`.
- Maintainers may push small fixes to your branch or squash commits when merging.
- Label suggestions (`bug`, `enhancement`, `new-tool`, `documentation`, `breaking`, …) are welcome. Labels sort the auto-generated release notes.

## Commit messages

Commits and pull request titles follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/). Pull requests are squash-merged, so the PR title becomes the commit on `main`, and a check fails the pull request if its title doesn't match.

- **Format:** `<type>(<optional scope>)<optional !>: <description>`, for example `feat: add WireGuard peer tools`, `fix(wifi): keep the AP group when updating a WLAN`, `docs: explain the TLS choice` or `build(deps): bump golang from 1.27 to 1.28`.
- **Types:** `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `build` (dependencies, Dockerfile and toolchain), `ci` (workflows), `chore` (anything else, such as `chore(release): v1.2.0`), `revert` and `style`.
- **Description:** imperative mood, lower-case first word and no trailing full stop. Keep the whole subject to 72 characters or fewer.
- **Breaking changes:** add `!` after the type or scope **and** a `BREAKING CHANGE: …` footer in the body or PR description, and label the pull request `breaking`.
- **Body:** explain **why** when it isn't obvious, and reference issues with `Fixes #123`.

## Adding or changing tools

The full guide is in [docs/development.md](docs/development.md#adding-a-tool). The short version:

- Put the tool in the right toolset file under `internal/tools/`, using `c.add()`.
- Mark it correctly. `Gate: Write` for anything that changes state, `Gate: Delete` for deletes, and `Destructive: boolPtr(true)` for writes that disconnect clients, reboot devices or change configuration. **This gating is the project's main safety feature, so reviewers check it carefully.**
- Follow the `save_*` convention for configuration objects: no `id` creates, and an `id` updates only the fields passed.
- Return compact summaries, offer `raw: true`, and redact secrets.
- Write the description for an AI model: what the tool does, side effects, units and version requirements.
- Add tests against the mock controller, then run `make docs`.

Changing an existing tool's name or arguments in an incompatible way is a **breaking change**. Call it out in the PR and the changelog.

## Releases

Releases are made by maintainers by publishing a GitHub release. That triggers the workflow that builds and pushes the Docker image. The changelog update for a release goes in a small pull request titled `chore(release): vX.Y.Z`. See [docs/releasing.md](docs/releasing.md).

## Getting help

Ask in [GitHub Discussions](https://github.com/mattoddie/unifi-mcp/discussions), or comment on the issue you're working on. No question is too small.
