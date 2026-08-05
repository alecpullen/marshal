# Marshal Web UI

A browser-based client for the Marshal webbridge. Built with Svelte 5, Vite,
and TypeScript.

## Development

```bash
cd web/ui
npm install
npm run dev
```

The dev server proxies API requests to the webbridge running on
`http://127.0.0.1:7700` (configure in `vite.config.ts` if needed).

## Build

```bash
cd web/ui
npm run build
```

The build writes static assets into `web/bridge/static/`, replacing the
placeholder `index.html`. Rebuild `webbridge` to embed the real UI:

```bash
go build ./cmd/webbridge
```

## Tests

```bash
cd web/ui
npm test
```

Vitest runs unit tests for the session store and SSE parsing. There is no
e2e test suite for v1.