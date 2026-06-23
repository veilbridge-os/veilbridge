# internal/api

Router Core API — the REST/JSON contract the web UI talks to. This is the *only*
surface the frontend sees; no OS specifics leak through here.

**Code-first with [Huma](https://github.com/danielgtaylor/huma).** Handlers are typed
`func(ctx, *In) (*Out, error)` over `net/http`; OpenAPI 3.1 + request validation are
generated from the Go types. The Go code is the source of truth.

[`api/openapi.yaml`](../../api/openapi.yaml) is a generated **snapshot**, checked in
CI against the code — not edited by hand. It feeds the frontend's typed client.

The live `/openapi.json` and `/docs` (Swagger UI) endpoints are **dev-only** (build
tag `dev` / config flag, off by default) — the production router binary ships
without them. See `CONTRIBUTING.md` §3 (D-8, D-9).
