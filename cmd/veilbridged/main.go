// Command veilbridged is the VeilBridge agent daemon. It detects the platform,
// builds the matching adapter, and serves the Router Core API (and, later, the
// embedded web UI) over HTTP. See the architecture notes in CONTRIBUTING.md
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/adapters"
	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt"
	"github.com/veilbridge-os/veilbridge/internal/api"
	"github.com/veilbridge-os/veilbridge/internal/buildinfo"
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
		showVersion = flag.Bool("version", false, "print the build version and exit")
		// Emergency access (#38, D-77): a way back when a network change
		// stuck and took the panel with it. They work without the panel's
		// own configuration and never start the server.
		restoreNet   = flag.Bool("restore-network", false, "put back the network, firewall and address-handout settings from before the last change that stuck, and exit")
		restorePoint = flag.String("restore-point", "", "like -restore-network, from this restore point (see -list-restore-points)")
		listPoints   = flag.Bool("list-restore-points", false, "list the restore points and what differs in each from the settings now, and exit")
		force        = flag.Bool("force", false, "with -restore-network: restore even while a change waits for confirmation")
	)
	flag.Parse()

	build := buildinfo.Read()

	// -version must answer without touching the config or the host, so that it
	// works on a fresh machine and in a release smoke test.
	if *showVersion {
		if _, err := io.WriteString(os.Stdout, build.String()+"\n"); err != nil {
			log.Fatalf("version: %v", err)
		}
		return
	}

	if *restoreNet || *restorePoint != "" || *listPoints {
		os.Exit(emergencyRestore(*listPoints, *restorePoint, *force))
	}

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

	// Before serving anything: undo a configuration change that was applied but
	// never confirmed by a previous run. The daemon disappearing inside the
	// confirmation window is exactly the case the watchdog exists for — a
	// reboot or a crash caused by the change being tested. Not fatal: a router
	// that cannot undo still has to come up and say so, because a panel that
	// refuses to start is a panel nobody can fix the router from.
	if reverted, err := srv.RecoverPendingApply(); err != nil {
		log.Printf("WARNING: a configuration change was applied but never confirmed, "+
			"and it could not be undone: %v", err)
	} else if reverted {
		log.Println("undid a configuration change that was never confirmed " +
			"(the daemon stopped inside the confirmation window)")
	}

	// Start recording the device's vitals before serving. Sampling is not tied
	// to a connected client: a dashboard opened at 09:00 has to show what the
	// router was doing at 08:00, and a graph that only records while somebody
	// watches is flat every time anyone looks (D-13, M2.2).
	stopSampling := srv.StartSampling()
	defer stopSampling()

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
		log.Printf("%s listening on %s (platform=%s, dev=%v)", build, addr, adapter.Platform(), *dev)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down…")

	// Live streams (SSE) are long-lived by design, so a graceful shutdown will
	// routinely hit this deadline rather than exceptionally: Shutdown waits for
	// open connections, and an EventSource holds one open for as long as the
	// tab is. The timeout is what turns "wait for clients" into "leave anyway".
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("shutdown (live streams may have been cut): %v", err)
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

// emergencyRestore runs the emergency command (#38) and returns the exit
// code: 0 done or nothing to do, 2 a change is waiting for confirmation (the
// device undoes it by itself), 1 anything else.
func emergencyRestore(list bool, point string, force bool) int {
	err := openwrt.EmergencyRestore(openwrt.RestoreOptions{List: list, Point: point, Force: force}, os.Stdout)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, openwrt.ErrChangePending):
		return 2
	default:
		fmt.Fprintf(os.Stderr, "restore: %v\n", err)
		return 1
	}
}
