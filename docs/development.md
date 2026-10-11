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

You need Node.js 22 or later (CI tests 22 and 24) and Docker for building the image.

```sh
git clone https://github.com/mattoddie/unifi-mcp.git
cd unifi-mcp
npm ci
npm test            # type-checks, builds and runs all tests against a mock controller
```

| Script | What it does |
|---|---|
| `npm run build` | Compile TypeScript from `src/` to `dist/` |
| `npm test` | Build, then run `dist/test/*.test.js` with the Node test runner |
| `npm start` | Run the built server (`node dist/index.js`) |
| `npm run docs:tools` | Regenerate [`docs/tools.md`](tools.md) from the tool definitions |
| `npm run docs:check` | Fail if `docs/tools.md` is out of date (CI runs this) |

The project deliberately has only three runtime dependencies: `@modelcontextprotocol/sdk`, `zod` and `undici`. There's no test framework, linter or bundler. Please discuss before adding a dependency.

## Project layout

```text
src/
├── index.ts            # entry point: load config, pick transport, handle signals
├── config.ts           # environment variables → typed Config (validation lives here)
├── server.ts           # builds the McpServer and registers every toolset and prompt
├── http.ts             # stateless Streamable HTTP transport, auth, Host allow-list, /health
├── prompts.ts          # the built-in MCP prompts
├── unifi/
│   ├── client.ts       # UnifiClient: auth, sessions/CSRF, UniFi OS detection, errors
│   └── format.ts       # summarizers (devices, clients, ports…), redaction, unit helpers
├── tools/
│   ├── util.ts         # defineTool() and shared argument schemas
│   ├── rest.ts         # CRUD helpers for classic REST, v2 and settings endpoints
│   └── <toolset>.ts    # one file per toolset: overview, devices, clients, …
├── scripts/
│   └── gen-tool-docs.ts
└── test/
    ├── mock-controller.ts  # an in-process fake UniFi controller
    ├── helpers.ts          # connect an MCP client to a server, start the mock
    └── *.test.ts
```

## How a request flows

1. An MCP client calls a tool, for example `unifi_list_devices {type: "switch"}`.
2. The MCP SDK validates the arguments against the tool's zod schema.
3. The handler registered by `defineTool()` runs. It calls `UnifiClient`, usually through the helpers in `tools/rest.ts`.
4. `UnifiClient.request()` adds the `/proxy/network` prefix on UniFi OS, authenticates (API key, or session cookie plus CSRF, logging in again on 401), sends the request and unwraps the `{meta, data}` envelope. It turns failures into a `UnifiError` with a helpful hint.
5. The handler summarises the result (`unifi/format.ts`), redacts secrets and returns plain data.
6. `defineTool()` serialises that to JSON text. A thrown error becomes `isError: true` with the message, so the model can see what went wrong and adjust.

## Adding a tool

Tools are defined with `defineTool()` in the file for their toolset. Here's a complete example:

```ts
defineTool(ctx, "unifi_save_bandwidth_profile", {
  toolset: "networks",                       // which UNIFI_TOOLSETS entry enables it
  title: "Create/update bandwidth profile",  // short human title
  description:
    "Create (no id) or update (id) a bandwidth profile (user group). Rates are in kbps; -1 means unlimited.",
  write: true,                               // only registered with UNIFI_ALLOW_WRITES=true
  input: {
    ...siteArg,
    id: idArg("Bandwidth profile").optional().describe("Profile ID to update; omit to create"),
    name: z.string().min(1).optional().describe("Profile name (required on create)"),
    download_kbps: z.number().int().min(-1).optional().describe("Download cap in kbps (-1 = unlimited)"),
    upload_kbps: z.number().int().min(-1).optional().describe("Upload cap in kbps (-1 = unlimited)"),
  },
  handler: async (a) => {
    if (!a.id && !a.name) throw new Error("name is required when creating a bandwidth profile");
    const body = defined({ name: a.name, qos_rate_max_down: a.download_kbps, qos_rate_max_up: a.upload_kbps });
    return summarizeUsergroup(await restSave(client, site(a.site), "usergroup", a.id, body));
  },
});
```

The `defineTool()` options that control registration:

| Option | Effect |
|---|---|
| `toolset` | Required. The tool is only registered when this toolset is enabled. |
| `write: true` | The tool changes state. Only registered when writes are enabled. Marked `readOnlyHint: false`. |
| `delete: true` | The tool deletes configuration. Only registered when deletes are enabled too. Marked destructive. |
| `destructive: true` | Marks a write tool as destructive (`destructiveHint`), so clients confirm before running it. Use it for anything that disconnects clients, reboots devices or changes configuration. |
| `readOnly` | Overrides the read-only hint. This is rarely needed. |

Checklist for a new tool:

1. Put it in the right `src/tools/<toolset>.ts`. If it needs a new toolset, add it to `TOOLSETS` in `config.ts` and register it in `server.ts`.
2. Use the shared arguments (`siteArg`, `idArg`, `macArg`, `searchArg`, `limitArg`, `extraArg`) so tools behave consistently.
3. For configuration objects, follow the **`save_*` convention**: no `id` creates, `id` updates only the fields passed (`restSave`/`v2Save` do this), and offer `extra` for raw fields.
4. Return a **compact summary** and offer `raw: true` for the full object. Run anything that might contain secrets through `redactSecrets()`.
5. Write descriptions for the model: say what the tool does, when to use it, units, side effects (*"clients will briefly disconnect"*), and any version requirement (*"Network 9.0+"*).
6. Add tests in `src/test/` using the mock controller.
7. Run `npm run docs:tools` and commit the updated `docs/tools.md`. If it's a notable feature, update the README table and [CHANGELOG](../CHANGELOG.md).

## Tool design guidelines

- **Fewer, broader tools beat many narrow ones.** One `save_wlan` with optional fields is easier for a model than `create_wlan`, `rename_wlan` and `set_wlan_password`.
- **Never surprise.** Updates only change what was asked. Destructive actions are flagged. Deletes are behind their own switch.
- **Errors should teach.** Throw messages that tell the model, or the user, how to fix the call: *"name is required when creating a bandwidth profile"*.
- **Be honest about uncertainty.** UniFi's internal APIs aren't documented. If you aren't sure of a field name across versions, say so in the description and point to `extra`.

## Testing

Tests use Node's built-in test runner against `MockController` (`src/test/mock-controller.ts`), a small HTTP server that imitates a UniFi OS console or legacy controller:

- Built-in login flows for both controller types, sessions, CSRF checks and API keys
- A generic in-memory CRUD store for `rest/<collection>` and `v2/<collection>`, which you fill with `seed()`
- `on(method, regex, handler)` to add custom routes, which take priority over the built-in ones
- `requestsTo(prefix)` to check what the server sent

A typical test:

```ts
import { connect, parse, startMock } from "./helpers.js";

it("creates a bandwidth profile", async () => {
  const { mock, client, stop } = await startMock();
  mock.seed("rest", "usergroup", []);
  const mcp = await connect(client, true); // true = writes enabled
  const res = parse(await mcp.callTool({ name: "unifi_save_bandwidth_profile", arguments: { name: "Kids", download_kbps: 10000 } }));
  assert.equal(res.name, "Kids");
  assert.equal(mock.requestsTo("/api/s/default/rest/usergroup")[0].body.qos_rate_max_down, 10000);
  await mcp.close();
  await stop();
});
```

Run a single file with `npm run build && node --test dist/test/networks.test.js`.

## Running against a real controller

With the [MCP Inspector](https://github.com/modelcontextprotocol/inspector):

```sh
npm run build
UNIFI_URL=https://192.168.1.1 UNIFI_API_KEY=... npx @modelcontextprotocol/inspector node dist/index.js
```

Or build the image and point Claude Code at it:

```sh
docker build -t unifi-mcp:dev .
docker run --rm -p 8080:8080 --env-file .env unifi-mcp:dev
claude mcp add --transport http unifi-dev http://localhost:8080/mcp --header "Authorization: Bearer $MCP_AUTH_TOKEN"
```

Please test write tools on a lab site or a non-critical object, and take a backup first.

## Docs that are generated

[`docs/tools.md`](tools.md) is generated by `src/scripts/gen-tool-docs.ts`. The script starts the server in-process with every toolset, writes and deletes enabled, and lists the registered tools and prompts. Don't edit `docs/tools.md` by hand. Change the tool's title, description or argument `.describe()` text and run `npm run docs:tools`. CI fails if the committed file is stale.

## The website

The site at [unifi-mcp.mattoddie.dev](https://unifi-mcp.mattoddie.dev) lives in [`website/`](../website/README.md). It has a hand-built landing page plus these docs rendered with Astro Starlight. It has its own `package.json`, so its dependencies never reach the server or the Docker image. The Markdown in `docs/` is the single source: edit it here and the site picks it up on the next build.
