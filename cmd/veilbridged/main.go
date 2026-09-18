// Command veilbridged is the VeilBridge agent daemon. It detects the platform,
// builds the matching adapter, and serves the Router Core API (and, later, the
// embedded web UI) over HTTP. See the architecture notes in CONTRIBUTING.md
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/adapters"
	"github.com/veilbridge-os/veilbridge/internal/api"
	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/core/mock"
)

func main() {
	var (
		configPath  = flag.String("config", config.DefaultPath, "path to the config file")
		listen      = flag.String("listen", "", "HTTP listen address (overrides config; default :8080)")
		dev         = flag.Bool("dev", false, "enable the OpenAPI spec + Swagger UI endpoints (dev only)")
		setPass     = flag.String("set-password", "", "set the admin password and exit")
		dumpOpenAPI = flag.Bool("dump-openapi", false, "print the generated OpenAPI YAML and exit (CI snapshot)")
		demo        = flag.Bool("demo", false, "serve sample data from an in-memory adapter: no OS access, no tunnels (UI development, screenshots, trying the panel without hardware)")
	)
	flag.Parse()

	store := config.NewStore(*configPath)

	// CI snapshot: print the code-generated OpenAPI spec and exit (D-8).
	if *dumpOpenAPI {
		if err := dumpSpec(store); err != nil {
			log.Fatalf("dump-openapi: %v", err)
		}
		return
	}

	// Bootstrap: set the admin password and exit (first-run setup).
	if *setPass != "" {
		if err := setPassword(store, *setPass); err != nil {
			log.Fatalf("set-password: %v", err)
		}
		log.Println("admin password set")
		return
	}

	doc, err := store.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if doc.Settings.PasswordHash == "" {
		log.Println("WARNING: no admin password set — the API will reject all logins. " +
			"Run with -set-password <pw> first.")
	}

	var adapter core.Adapter
	if *demo {
		// Demo mode touches nothing on the host: no uci, no nftables, no TUN.
		// Sample addresses come from the RFC 5737 documentation ranges.
		log.Println("DEMO MODE: sample data, no OS access and no real tunnels")
		adapter = mock.NewDemoAdapter()
	} else {
		adapter, err = adapters.New(*configPath)
		if err != nil {
			log.Fatalf("init adapter: %v", err)
		}
	}

	srv, err := api.New(adapter, store, api.Options{Dev: *dev})
	if err != nil {
		log.Fatalf("init api: %v", err)
	}

	addr := *listen
	if addr == "" {
		addr = doc.Settings.ListenAddr
	}
	if addr == "" {
		addr = ":8080"
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		log.Printf("veilbridged listening on %s (platform=%s, dev=%v)", addr, adapter.Platform(), *dev)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down…")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// openAPIHeader is prepended to the dumped spec so the committed
// api/openapi.yaml self-documents as generated. It must match the file's header
// byte-for-byte, since CI diffs the two (see .github/workflows/ci.yml).
const openAPIHeader = `# GENERATED — do not edit by hand.
# Produced from the Go handler types (code-first, Huma — DESIGN D-8) via:
#   go run ./cmd/veilbridged -dump-openapi > api/openapi.yaml
# CI regenerates this and fails on drift (git diff --exit-code).
`

// dumpSpec prints the code-generated OpenAPI YAML to stdout. It uses a mock
// adapter so the spec is platform-independent (it describes shapes, not state).
func dumpSpec(store *config.Store) error {
	srv, err := api.New(mock.NewAdapter(), store, api.Options{Dev: true})
	if err != nil {
		return err
	}
	spec, err := srv.OpenAPIYAML()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(os.Stdout, openAPIHeader); err != nil {
		return err
	}
	_, err = os.Stdout.Write(spec)
	return err
}

func setPassword(store *config.Store, pw string) error {
	doc, err := store.Load()
	if err != nil {
		return err
	}
	if err := doc.SetPassword(pw); err != nil {
		return err
	}
	return store.Save(doc)
}
