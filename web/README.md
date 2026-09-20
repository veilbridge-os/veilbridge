# web

Vue 3 + Element Plus frontend. Built to static assets and embedded into the Go
binary via `go:embed`. The UI communicates only through the Router Core API.

## Develop

```sh
npm install
npm run dev          # Vite on :5173, proxies /api → the Go agent on :8080
```

Run the agent separately: `go run ./cmd/veilbridged -dev -set-password <pw>` then start it.

To develop against a remote agent (e.g. a test stand) instead of a local one,
point the dev proxy at it:

```sh
VB_API_TARGET=http://192.0.2.10:8080 npm run dev
```

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
never drifts from the backend contract.

## i18n

Localized with vue-i18n. Two layers move under one selected locale:

- **our strings** — `src/i18n/messages/*.ts` (vue-i18n);
- **Element Plus components** — applied via `<el-config-provider :locale>` in App.vue.

13 languages are offered (en, ru, de, fr, es, it, pt, pl, uk, tr, sv, da, fi) —
the same set Keenetic's web UI exposes, and all of them exist in Element Plus.
`en` and `ru` are fully translated; the rest fall back to `en` for our strings
(Element Plus components are already localized for all 13). Add a language by
filling in a `messages/<code>.ts` bundle.

Type-safe: `t()` keys are checked against the English schema (`i18n/types.d.ts`),
and every locale bundle must `satisfy` `MessageSchema` — a missing or misspelled
key is a compile error, not a silent runtime fallback. The choice is persisted in
localStorage and defaults to the browser language when supported.

## Toolchain versions

Dependencies are pinned to exact versions (no `^`): the panel is embedded into
a release binary, and a transitive bump that changes the bundle should be a
commit, not a coincidence of when someone ran `npm install`.

### Why TypeScript is 5.9 and not 7.x

TypeScript 7 is the native rewrite, and it cannot type-check this project yet.
Measured rather than assumed:

- `vue-tsc` resolves `typescript/lib/tsc` to patch the compiler; TS 7 does not
  export it, and `npm run build` fails with `ERR_PACKAGE_PATH_NOT_EXPORTED`.
- Running `tsc` from TS 7 directly "succeeds" — and that success is empty:
  `--listFiles` shows it loaded only `.d.ts` files and not one `.vue` source.
  A type check that silently covers nothing is worse than one that fails.

So the pin stays at the newest version that actually checks the code. The test
for lifting it: `vue-tsc` builds against TS 7, and a deliberate type error
inside a `.vue` file still fails the build — which is how the current pin was
verified.
