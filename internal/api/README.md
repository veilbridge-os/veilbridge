# internal/api

Router Core API — the REST/JSON contract the web UI talks to. This is the *only*
surface the frontend sees; no OS specifics leak through here.

The contract is formally specified in [`api/openapi.yaml`](../../api/openapi.yaml)
(OpenAPI 3.1). That file is the source of truth — server stubs (oapi-codegen) and
the frontend's typed client are generated from it. See `CONTRIBUTING.md` §3.
