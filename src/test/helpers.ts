import assert from "node:assert/strict";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { loadConfig, TOOLSETS, type ToolOptions } from "../config.js";
import { createServer } from "../server.js";
import { UnifiClient } from "../unifi/client.js";
import { MockController } from "./mock-controller.js";

export function toolOptions(allowWrites: boolean, allowDeletes = false): ToolOptions {
  return { allowWrites, allowDeletes, toolsets: new Set(TOOLSETS) };
}

export async function connect(client: UnifiClient, options: ToolOptions | boolean): Promise<Client> {
  const server = createServer(client, typeof options === "boolean" ? toolOptions(options) : options);
  const [a, b] = InMemoryTransport.createLinkedPair();
  await server.connect(a);
  const mcp = new Client({ name: "test", version: "0" });
  await mcp.connect(b);
  return mcp;
}

/** Parse a successful tool result's JSON text, failing the test on isError. */
export function parse(result: any): any {
  assert.equal(result.isError, undefined, result.content?.[0]?.text);
  return JSON.parse(result.content[0].text);
}

/** Start an API-key UniFi OS mock and a client pointed at it. Call the returned stop() in after(). */
export async function startMock(): Promise<{ mock: MockController; client: UnifiClient; stop: () => Promise<void> }> {
  const mock = new MockController({ mode: "unifi-os", apiKey: "key123" });
  await mock.start();
  const client = new UnifiClient(loadConfig({ UNIFI_URL: mock.url, UNIFI_API_KEY: "key123" }).unifi);
  return {
    mock,
    client,
    stop: async () => {
      await client.close();
      await mock.stop();
    },
  };
}
