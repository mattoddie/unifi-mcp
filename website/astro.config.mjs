// @ts-check
import starlight from "@astrojs/starlight";
import { readFileSync } from "node:fs";
import { defineConfig } from "astro/config";

const GITHUB = "https://github.com/mattoddie/unifi-mcp";
const toolCount = (readFileSync(new URL("../docs/tools.md", import.meta.url), "utf8").match(/^### unifi_/gm) ?? []).length;

export default defineConfig({
  site: "https://unifi-mcp.mattoddie.dev",
  trailingSlash: "always",
  integrations: [
    starlight({
      title: "unifi-mcp",
      description: "An open-source MCP server for Ubiquiti UniFi Network. Monitor and manage your network from Claude and other AI assistants.",
      logo: { src: "./src/assets/logo.svg" },
      favicon: "/favicon.svg",
      social: [{ icon: "github", label: "GitHub", href: GITHUB }],
      customCss: ["./src/styles/docs.css"],
      lastUpdated: false,
      credits: false,
      head: [
        { tag: "link", attrs: { rel: "preconnect", href: "https://fonts.googleapis.com" } },
        { tag: "link", attrs: { rel: "preconnect", href: "https://fonts.gstatic.com", crossorigin: true } },
        {
          tag: "link",
          attrs: { rel: "stylesheet", href: "https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" },
        },
        { tag: "meta", attrs: { property: "og:image", content: "https://unifi-mcp.mattoddie.dev/og.png" } },
        { tag: "meta", attrs: { name: "twitter:card", content: "summary_large_image" } },
      ],
      sidebar: [
        { label: "Overview", link: "/docs/" },
        {
          label: "Start here",
          items: ["docs/getting-started", "docs/clients", "docs/configuration"],
        },
        {
          label: "Using unifi-mcp",
          items: ["docs/usage", { label: "Tool reference", link: "/docs/tools/", badge: { text: String(toolCount), variant: "note" } }],
        },
        {
          label: "Running it well",
          items: ["docs/deployment", "docs/security", "docs/compatibility", "docs/troubleshooting"],
        },
        {
          label: "Project",
          items: ["docs/contributing", "docs/development", "docs/releasing", "docs/security-policy", "docs/changelog"],
        },
      ],
    }),
  ],
});
