# unifi-mcp website

The site at [unifi-mcp.mattoddie.dev](https://unifi-mcp.mattoddie.dev): a hand-built landing page at `/` and the documentation at `/docs/`, built with [Astro](https://astro.build) and [Starlight](https://starlight.astro.build).

```sh
cd website
npm ci
npm run dev       # http://localhost:4321
npm run build     # static site in dist/
```

## How it fits together

| Path | What it is |
|---|---|
| `src/pages/index.astro` | The landing page (HTML, CSS and a little JS, no framework) |
| `src/lib/tools.ts` | Reads tool and toolset counts from `../docs/tools.md` at build time |
| `scripts/sync-docs.mjs` | Copies `../docs/*.md`, `CONTRIBUTING.md`, `CHANGELOG.md` and `SECURITY.md` into Starlight, runs before every `dev` and `build` |
| `astro.config.mjs` | Starlight config: sidebar, logo, social links |
| `src/styles/docs.css` | Brand colours for the docs theme |
| `public/og.png` | Social preview image, rendered from `scripts/og.html` |

**Don't edit docs here.** The Markdown in the repository's `docs/` folder is the source, and `src/content/docs/` is generated and not committed. Edit `docs/*.md`, and the change appears on GitHub and on the site.

To change the social preview, edit `scripts/og.html`, screenshot it at 1200×630 (for example with `npx playwright screenshot --viewport-size=1200,630`), and save the result as `public/og.png`.

## Deployment

Cloudflare Pages builds the site from this folder on every push. Pushes to `main` deploy to production, and pull requests get a preview URL. It only rebuilds when `website/`, `docs/`, `CHANGELOG.md`, `CONTRIBUTING.md` or `SECURITY.md` changes, because the site publishes those root files too.

| Setting | Value |
|---|---|
| Root directory | `website` |
| Build command | `npm run build` |
| Output directory | `dist` |
| Node version | `NODE_VERSION=24` |
