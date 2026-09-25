# cmd/veilbridged

Entry point for the VeilBridge agent daemon. Wires together the Router Core API,
the embedded web UI (built with `-tags ui`), and the platform adapter selected at
runtime (OpenWrt only; anything else is refused).

Flags:
- `-config <path>` — config file (default `/etc/veilbridge/config.json`).
- `-set-password <pw>` — set the admin password and exit (first-run setup).
- `-listen <addr>` — HTTP listen address (overrides config; default `:8080`).
- `-dev` — enable the OpenAPI spec + Swagger UI endpoints (off in production, D-9).
- `-dump-openapi` — print the code-generated OpenAPI YAML and exit (CI snapshot, D-8).
- `-demo` — serve sample data from an in-memory adapter: no OS access, no
  tunnels. The way to run the panel off a router (UI work, screenshots).
- `-version` — print the build version and exit.

Graceful shutdown on SIGINT/SIGTERM.
