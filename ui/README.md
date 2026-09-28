# Stackorder web UI

The single-page app that the Stackorder server embeds and serves under `/`.
It is read-only apart from two actions, unlocking a stack and re-running a
run, and both go through the same JSON API as every other client.

Built with Preact, `preact-iso` for routing, `@dagrejs/dagre` for graph
layout with hand-written SVG, and one plain stylesheet. It uses no
component library and loads no fonts or scripts from a CDN.

## Pages

| Path | Data | Shows |
| --- | --- | --- |
| `/` | `GET /v1/overview` | Counts, stacks and runs by status, drift, locks held, recent runs |
| `/repos` | `GET /v1/repos` | Repositories with stack, drift and lock counts |
| `/repos/:owner/:repo` | `GET /v1/repos/{owner}/{repo}/graph?ref=&run=`, `.../runs`, `GET /v1/runs/{id}` | The dependency graph. `?run=` replays that run's affected set and waves |
| `/stacks/:id` | `GET /v1/stacks/{id}`, `.../runs`, `GET /v1/repos/{owner}/{repo}/stacks` | Last apply and plan, drift, lock and unlock, dependencies, pinned modules, history |
| `/runs/:id` | `GET /v1/runs/{id}` | Waves with per-stack results, plan details, job logs, re-run. Refreshes every 10 s until the run is terminal |
| `/modules` | `GET /v1/modules` | Every module; `?q=` filters by key or source |
| `/modules/:id` | `GET /v1/modules/{id}` | Released versions and consumers with how far behind they pin |

The shell calls `GET /v1/me` first. A 401 shows only a "Sign in with GitHub"
link to `/auth/login`, and "Sign out" posts to `/auth/logout`.

### Reading the graph

Stacks are rounded rectangles and modules are hexagons. Edges that carry
`inferred: true`, such as the `reads_state` edges found from
`terraform_remote_state`, are dashed; `depends_on` edges and a
`reads_state` edge a stack config made explicit are solid, and
`uses_module` edges are dotted grey. Arrows point from a dependency to what
depends on it, which is the direction a change propagates and applies run,
so the graph reads left to right like the waves.

When a run is replayed, the affected stacks are coloured by their status in
that run and badged with their wave (`W0`, `W1`, ...), modules on the change
path are highlighted, and everything else is dimmed. The side panel lists
the same waves as text. Scroll to zoom, drag the background to pan, or use
the toolbar. Every node is a link you can reach with Tab and open with
Enter.

## Developing

Node 24 and npm.

```sh
npm ci
npm run dev        # Vite on http://localhost:5173
```

The dev server proxies `/v1` and `/auth` to a server on
`http://localhost:8080`, which `make dev` at the repository root starts.
Browsers scope cookies by host, not port, so a session started on either
port works on both.

## Checks

```sh
npm run lint       # ESLint with typescript-eslint, no warnings allowed
npm run typecheck  # tsc for the app and for the Node-side config and tests
npm test           # Vitest unit tests in jsdom
npm run build      # writes ../internal/ui/dist
npm run test:e2e   # builds, starts vite preview and runs Playwright
```

Browser tests need Chromium once: `npx playwright install chromium`.

Unit tests render pages against a fake `fetch`, and the browser tests mock
the API with `page.route`. Both serve the same fixtures from
`src/fixtures/`: the design document's example graph in `acme/infra` and a
three-wave apply of PR #42 in which `stacks/prod/eks` fails and
`stacks/prod/apps` is blocked. `go test ./internal/ui/...` checks that
every fixture decodes into its `api/v1` type with nothing unknown and
nothing the server would not send.

## API types

`src/api/types.ts` mirrors `api/v1/types.go` by hand, with field names
identical to the JSON tags. A field without `omitempty` that holds a slice,
map or pointer is typed `| null`, because Go encodes a nil value as `null`.
When `api/v1` changes, update `types.ts` in the same commit; the Go test in
`internal/ui` fails until the two agree.

## How the server serves it

`npm run build` empties `internal/ui/dist` (keeping `.gitkeep`) and writes
`index.html` plus content-hashed files under `assets/`. The server embeds
that directory with `//go:embed`, so rebuild the server after rebuilding the
UI; from the repository root, `make ui` does the npm part. `internal/ui`
serves `assets/` with `Cache-Control: immutable` and other files with
`no-cache`. A missing file under `assets/`, or a missing top-level file
such as `/robots.txt`, is 404; any other path that is not a file of the
build gets `index.html`, so client routes survive a reload, including
repositories whose names end in `.js` or `.json`. A binary built without
the UI answers with a short plain-text page pointing at `make ui`.
