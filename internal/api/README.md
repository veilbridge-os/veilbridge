# internal/api

Router Core API — the REST/JSON contract the web UI talks to. This is the *only*
surface the frontend sees; no OS specifics leak through here.

**Code-first with [Huma](https://github.com/danielgtaylor/huma).** Handlers are typed
`func(ctx, *In) (*Out, error)` over `net/http`; OpenAPI 3.1 + request validation are
generated from the Go types. The Go code is the source of truth.

[`api/openapi.yaml`](../../api/openapi.yaml) is a generated **snapshot**, checked in
CI against the code — not edited by hand. It feeds the frontend's typed client.

The live `/openapi.json` and `/docs` (Swagger UI) endpoints are **dev-only**
(`Options.Dev` / the daemon's `-dev` flag, off by default) — production ships
without them. Regenerate the snapshot with `veilbridged -dump-openapi`. See
`CONTRIBUTING.md` §3 (D-8, D-9).

Files:
- `server.go` — Huma API: operation registration, per-op JWT middleware
  (`/auth/login` is the one open endpoint), handlers delegating to the adapter's
  managers, `OpenAPIYAML()` for the snapshot.
- `dto.go` — typed input/output shapes (Huma derives schemas from these).
- `tokens.go` — HS256 JWT issue/verify; signing key derived from the bcrypt
  password hash, so changing the password invalidates outstanding tokens.

Tested end-to-end over httptest against the mock adapter (`internal/core/mock`).
