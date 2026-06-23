# web

Vue 3 + Element Plus frontend. Built to static assets and embedded into the Go
binary via `go:embed`. The UI communicates only through the Router Core API.

## Develop

```sh
npm install
npm run dev          # Vite on :5173, proxies /api → the Go agent on :8080
```
Run the agent separately: `go run ./cmd/veilbridged -dev -set-password <pw>` then start it.

## Build into the binary

```sh
npm run gen-api      # regenerate src/api/schema.ts from ../api/openapi.yaml
npm run build        # outputs to ../internal/api/dist
cd .. && go build -tags ui ./cmd/veilbridged   # embeds dist into the binary
```

Without `-tags ui` the agent serves the API only (no UI) — handy for dev and for
building without Node. `dist/` is generated (gitignored); CI runs `npm run build`
then `go build -tags ui`.

## Screens
- **Login** — JWT (token in localStorage).
- **Dashboard** — live tiles (egress geo/IP, tunnel up, CPU, memory), 5s polling.
- **Nodes** — import `.conf`, activate (switches egress), remove; handshake age.
- **Routing** — add/delete domain|subnet → tunnel|direct rules, Apply, and Probe
  (egress-comparing path check, D-5).

Types in `src/api/schema.ts` are generated from `api/openapi.yaml` — the frontend
never drifts from the backend contract (D-8).
