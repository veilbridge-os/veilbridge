# cmd/veilbridged

Entry point for the VeilBridge agent daemon. Wires together the Router Core API,
the embedded web UI (Phase 7), and the platform adapter selected at runtime.

Flags:
- `-config <path>` — config file (default `/etc/veilbridge/config.json`).
- `-set-password <pw>` — set the admin password and exit (first-run setup).
- `-listen <addr>` — HTTP listen address (overrides config; default `:8080`).
- `-dev` — enable the OpenAPI spec + Swagger UI endpoints (off in production, D-9).
- `-dump-openapi` — print the code-generated OpenAPI YAML and exit (CI snapshot, D-8).

Graceful shutdown on SIGINT/SIGTERM.
