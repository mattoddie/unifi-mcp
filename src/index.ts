#!/usr/bin/env node
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { loadConfig } from "./config.js";
import { startHttpServer } from "./http.js";
import { createServer, SERVER_VERSION } from "./server.js";
import { UnifiClient } from "./unifi/client.js";

async function main(): Promise<void> {
  const cfg = loadConfig();
  const client = new UnifiClient(cfg.unifi);
  // Logs go to stderr: stdout is the MCP channel in stdio mode.
  console.error(
    `unifi-mcp ${SERVER_VERSION}: controller ${cfg.unifi.url}, site "${cfg.unifi.site}", auth ${cfg.unifi.apiKey ? "API key" : "username/password"}, ` +
      `writes ${cfg.tools.allowWrites ? "ENABLED" : "disabled"}${cfg.tools.allowDeletes ? " (with deletes)" : ""}, transport ${cfg.transport}`,
  );

  let shutdown: () => Promise<void>;
  if (cfg.transport === "http") {
    const httpServer = await startHttpServer(cfg, () => createServer(client, cfg.tools));
    shutdown = async () => {
      httpServer.close();
      await client.close();
    };
  } else {
    const server = createServer(client, cfg.tools);
    await server.connect(new StdioServerTransport());
    shutdown = async () => {
      await server.close();
      await client.close();
    };
  }

  for (const signal of ["SIGINT", "SIGTERM"] as const) {
    process.once(signal, () => {
      void shutdown().finally(() => process.exit(0));
    });
  }
}

main().catch((err) => {
  console.error(`unifi-mcp: ${err instanceof Error ? err.message : err}`);
  process.exit(1);
});
