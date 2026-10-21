# Development

This page is for people working on unifi-mcp itself. Start with [CONTRIBUTING.md](../CONTRIBUTING.md) for the workflow, and use this page for how the code fits together.

- [Setup](#setup)
- [Project layout](#project-layout)
- [How a request flows](#how-a-request-flows)
- [Adding a tool](#adding-a-tool)
- [Tool design guidelines](#tool-design-guidelines)
- [Testing](#testing)
- [Running against a real controller](#running-against-a-real-controller)
- [Docs that are generated](#docs-that-are-generated)
- [The website](#the-website)

## Setup

You need Go 1.25 or later, and Docker for building the image.

```sh
git clone https://github.com/mattoddie/unifi-mcp.git
cd unifi-mcp
make test           # runs all tests against a mock controller
```

| Target | What it does |
|---|---|
| `make build` | Build a static binary to `bin/unifi-mcp`. Pass `VERSION=1.2.3` to stamp a version. |
| `make test` | `go test ./...` |
| `make lint` | Fail if any file needs `gofmt`, then run `go vet ./...` |
| `make docs` | Regenerate [`docs/tools.md`](tools.md) from the tool definitions |
| `make docs-check` | Fail if `docs/tools.md` is out of date (CI runs this) |
| `make image` | Build the Docker image as `unifi-mcp:$(VERSION)` |

The project deliberately has a single direct dependency: the official MCP Go SDK, [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk). Everything else is the standard library. Please discuss before adding a dependency.

## Project layout

```text
cmd/
├── unifi-mcp/           # the binary: serve (default), healthcheck and version commands
└── gen-tool-docs/       # writes docs/tools.md from the registered tools and prompts
internal/
├── config/              # environment variables → typed Config (validation lives here)
├── ojson/               # JSON values with JavaScript semantics: ordered keys, undefined vs null, ?? and truthiness
├── unifi/
│   ├── client.go        # Client: auth, sessions/CSRF, UniFi OS detection, errors
│   ├── dryrun.go        # Call: records the changes sent, stops them in a dry run, previews and diffs
│   ├── paths.go         # path safety checks and encoding
│   ├── tls.go           # certificate verification, UNIFI_CA_FILE and UNIFI_TLS_FINGERPRINT pinning
│   ├── format.go        # summarizers (devices, clients, ports…), redaction, unit helpers
│   └── unifitest/       # an in-process fake UniFi controller for tests
├── httpserver/          # stateless Streamable HTTP, bearer auth, Host/Origin allow-list, /health
└── tools/
    ├── tools.go         # the registration framework: Def, Gate, add(), Args
    ├── guard.go         # dry_run and the audit log for tools that change things
    ├── schema.go        # input schemas (the JSON Schema clients see) and argument validation
    ├── args.go          # shared arguments: site, mac, id, extra, search, limit, raw
    ├── rest.go          # CRUD helpers for classic REST, v2 and settings endpoints
    ├── server.go        # NewServer(): server instructions, registers every toolset and the prompts
    ├── prompts.go       # the built-in MCP prompts
    ├── lazy.go          # lazy mode: the tool registry, search and the meta-tools
    └── <toolset>.go     # one file per toolset: overview, devices, clients, …
```

## How a request flows

1. An MCP client calls a tool, for example `unifi_list_devices {type: "switch"}`. In lazy mode (the default) it calls `unifi_call_tool {name: "unifi_list_devices", arguments: {type: "switch"}}` instead, which checks that the tool is read-only (`unifi_call_write_tool` checks that it isn't) and passes the arguments to the same handler as full mode.
2. `add()` validates the arguments against the tool's input schema (`schema.go`), drops unknown keys and returns a validation error as a tool result, with the same wording the server has always used.
3. The handler runs. It calls `unifi.Client`, usually through the helpers in `tools/rest.go`. For a tool that changes things, `guard.go` wraps it first: `add()` gives it the `dry_run` argument, and the wrapper around its runner puts a `unifi.Call` in the context to record the state-changing requests sent, and writes the audit log entry (`UNIFI_AUDIT_LOG`) when the call ends. Full mode and the lazy meta-tools call the same runner, so both behave the same way and log the tool's own name.
4. `Client.Request()` checks the path is safe, adds the `/proxy/network` prefix on UniFi OS, authenticates (API key, or session cookie plus CSRF, logging in again on 401 with a single shared login), sends the request and unwraps the `{meta, data}` envelope. It turns failures into a `unifi.Error` with a helpful hint.
5. The handler summarises the result (`unifi/format.go`), redacts secrets and returns plain data.
6. `add()` serialises that to indented JSON text with `ojson`, keeping field order. A returned error becomes `isError: true` with the message, so the model can see what went wrong and adjust.

Controller objects are handled as `ojson` values rather than Go structs: UniFi's internal APIs are undocumented and vary between versions, so the tools read the fields they need and pass everything else through untouched.

## Adding a tool

Tools are defined with `c.add()` in the file for their toolset. Here's a complete example:

```go
c.add(Def{
	Toolset:     "networks",                        // which UNIFI_TOOLSETS entry enables it
	Name:        "unifi_save_bandwidth_profile",
	Title:       "Create/update bandwidth profile", // short human title
	Description: "Create (no id) or update (id) a bandwidth profile (user group). Rates are in kbps; -1 means unlimited.",
	Gate:        Write,                             // only registered with UNIFI_ALLOW_WRITES=true
	Input: props(
		siteArg(),
		P("id", idSchema("Bandwidth profile").Opt().D("Profile ID to update; omit to create")),
		P("name", Str().MinLen(1).Opt().D("Profile name (required on create)")),
		P("download_kbps", Int().Min(-1).Opt().D("Download cap in kbps (-1 = unlimited)")),
		P("upload_kbps", Int().Min(-1).Opt().D("Upload cap in kbps (-1 = unlimited)")),
	),
	Handler: func(ctx context.Context, a Args) (any, error) {
		id := a.Str("id")
		if id == "" && !a.Truthy("name") {
			return nil, errorf("name is required when creating a bandwidth profile")
		}
		body := unifi.Defined("name", a.Get("name"), "qos_rate_max_down", a.Get("download_kbps"), "qos_rate_max_up", a.Get("upload_kbps"))
		saved, err := restSave(ctx, client, c.site(a), "usergroup", id, body)
		if err != nil {
			return nil, err
		}
		return summarizeUsergroup(saved), nil
	},
})
```

The `Def` fields that control registration:

| Field | Effect |
|---|---|
| `Toolset` | Required. The tool is only registered when this toolset is enabled. |
| `Gate: Write` | The tool changes state. Only registered when writes are enabled. Marked `readOnlyHint: false`. |
| `Gate: Delete` | The tool deletes configuration. Only registered when deletes are enabled too. Marked destructive. |
| `Destructive` | Marks a write tool as destructive (`destructiveHint`), so clients confirm before running it. Use it for anything that disconnects clients, reboots devices, changes configuration or grants access (admins, sites, guest authorization, vouchers, RADIUS accounts, unblocking). |
| `ReadOnly` | Overrides the read-only hint. This is rarely needed. |

Checklist for a new tool:

1. Put it in the right `internal/tools/<toolset>.go`. If it needs a new toolset, add it to `Toolsets` in `internal/config` and register it in `server.go`.
2. Use the shared arguments (`siteArg`, `idArg`, `macArg`, `searchArg`, `limitArg`, `extraArg`) so tools behave consistently.
3. For configuration objects, follow the **`save_*` convention**: no `id` creates, `id` updates only the fields passed (`restSave`/`v2Save` do this), and offer `extra` for raw fields.
4. Read before you write, and **mark reads that `POST`**. Any request other than `GET` counts as a change: a dry run stops at the first one and returns it as the preview. For a controller endpoint that only reads but is `POST`ed (`stat/report`, `stat/event`, v2 `system-log`, `cmd/sitemgr get-admins`…), use `SitePostRead`, `V2PostRead` or `CmdRead` (or `Options.Read`). `TestDryRunEveryWriteTool` fails if a dry run stops at a read.
5. Return a **compact summary** and offer `raw: true` for the full object. Run anything that might contain secrets through `unifi.RedactSecrets()`.
6. Write descriptions for the model. In lazy mode `unifi_search_tools` matches keywords against the name, title and description, and shows only the description's first sentence, so make that sentence say what the tool does on its own. Then say when to use it, units, side effects (*"clients will briefly disconnect"*), and any version requirement (*"Network 9.0+"*).
7. Add tests in `internal/tools` using the mock controller.
8. Run `make docs` and commit the updated `docs/tools.md`. If it's a notable feature, update the README table and [CHANGELOG](../CHANGELOG.md).

## Tool design guidelines

- **Fewer, broader tools beat many narrow ones.** One `save_wlan` with optional fields is easier for a model than `create_wlan`, `rename_wlan` and `set_wlan_password`.
- **Never surprise.** Updates only change what was asked. Destructive actions are flagged. Deletes are behind their own switch.
- **Errors should teach.** Throw messages that tell the model, or the user, how to fix the call: *"name is required when creating a bandwidth profile"*.
- **Be honest about uncertainty.** UniFi's internal APIs aren't documented. If you aren't sure of a field name across versions, say so in the description and point to `extra`.

## Testing

Tests are ordinary Go tests against `unifitest.Mock` (`internal/unifi/unifitest`), a small HTTP server that imitates a UniFi OS console or legacy controller:

- Built-in login flows for both controller types, sessions, CSRF checks and API keys
- A generic in-memory CRUD store for `rest/<collection>` and `v2/<collection>`, which you fill with `Seed()`
- `On(method, regex, handler)` to add custom routes, which take priority over the built-in ones
- `RequestsTo(prefix)` and `Last()` to check what the server sent, and `Observe(fn)` to see each request as it arrives (for example to fail a dry-run test on anything but a GET)
- `Delay(regex, d)` to slow down responses (for concurrency tests), and a `TLS` option to serve HTTPS with a certificate from `SelfSignedCert()`, generated in memory for each run

A typical test:

```go
func TestCreateBandwidthProfile(t *testing.T) {
	env := startMock(t, toolOptions(true, false)) // writes enabled, deletes not
	env.mock.Seed("rest", "usergroup", `[]`)
	res := env.call("unifi_save_bandwidth_profile", `{"name":"Kids","download_kbps":10000}`)
	eq(t, "name", get(res, "name"), "Kids")
	eq(t, "body", firstTo(env.mock, "/api/s/default/rest/usergroup", "POST").Body, js(`{"name":"Kids","qos_rate_max_down":10000,"qos_rate_max_up":-1}`))
}
```

Run one package or test with `go test ./internal/tools -run TestNetworks`. CI also runs the suite with `-race`.

## Running against a real controller

With the [MCP Inspector](https://github.com/modelcontextprotocol/inspector):

```sh
make build
UNIFI_URL=https://192.168.1.1 UNIFI_API_KEY=... UNIFI_VERIFY_SSL=false npx @modelcontextprotocol/inspector ./bin/unifi-mcp
```

Or build the image and point Claude Code at it:

```sh
docker build -t unifi-mcp:dev .
docker run --rm -p 8080:8080 --env-file .env unifi-mcp:dev
claude mcp add --transport http unifi-dev http://localhost:8080/mcp --header "Authorization: Bearer $MCP_AUTH_TOKEN"
```

Please test write tools on a lab site or a non-critical object, and take a backup first.

## Docs that are generated

[`docs/tools.md`](tools.md) is generated by `cmd/gen-tool-docs`. It starts the server in-process in full mode with every toolset, writes and deletes enabled, and lists the registered tools and prompts. Don't edit `docs/tools.md` by hand. Change the tool's title, description or argument `.D()` text and run `make docs`. CI fails if the committed file is stale (`make docs-check`).

## The website

The site at [unifi-mcp.mattoddie.dev](https://unifi-mcp.mattoddie.dev) lives in [`website/`](../website/README.md). It has a hand-built landing page plus these docs rendered with Astro Starlight. It is a separate Node project with its own `package.json`, so its dependencies never reach the server or the Docker image. The Markdown in `docs/` is the single source: edit it here and the site picks it up on the next build.
