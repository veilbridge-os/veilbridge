# internal/config

Agent configuration and persisted state — the single JSON document that is the
source of truth for nodes, routing rules, and settings (DESIGN §2, D-4).

- `config.go` — the `Document` model. Secrets (node keys, AmneziaWG obfuscation,
  the bcrypt admin password hash) live here and never cross the API boundary;
  `PublicNodes()` / `StoredNode.Public()` derive the API-safe `core.Node` view.
- `store.go` — `Store.Load`/`Save` with atomic writes (temp file + rename in the
  same dir, so a crash mid-write never corrupts the live config) and the
  bcrypt `SetPassword`/`VerifyPassword` helpers (D-6).

The whole `Document` is the backup/restore unit (FR-8). Depends only on stdlib +
`golang.org/x/crypto/bcrypt`.
