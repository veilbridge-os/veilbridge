// Command p4smoke is the Phase-4 risk-gate smoke test: it brings up a real
// AmneziaWG tunnel with the userspace netstackEngine and proves traffic actually
// egresses through it (not via the host WAN). This is the go/no-go check on the
// userspace engine. It is NOT part of the product build — run it manually:
//
//	go run ./cmd/p4smoke -conf node.conf -expect-egress 203.0.113.20
//
// See the project history Phase 4 and docs/embedding-notes.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/vpn/amneziawg"
)

func main() {
	confPath := flag.String("conf", "", "path to AmneziaWG .conf")
	expectEgress := flag.String("expect-egress", "", "egress IP expected through the tunnel (the node's public IP)")
	flag.Parse()

	if *confPath == "" || *expectEgress == "" {
		log.Fatal("usage: p4smoke -conf <file> -expect-egress <node-ip>")
	}

	raw, err := os.ReadFile(*confPath)
	if err != nil {
		log.Fatalf("read conf: %v", err)
	}

	node, sec, err := amneziawg.Parse("p4-smoke", raw)
	if err != nil {
		log.Fatalf("parse conf: %v", err)
	}
	fmt.Printf("parsed node: id=%s endpoint=%s obfuscation_keys=%d\n",
		node.ID, node.Endpoint, len(sec.Obfuscation))

	// Baseline: who are we WITHOUT the tunnel (direct host egress)?
	direct := egressIP(http.DefaultClient.Do)
	fmt.Printf("direct egress (no tunnel): %s\n", direct)

	eng := amneziawg.NewNetstackEngine()
	cfg := amneziawg.ToNodeConfig(node.Endpoint, sec)
	if err := eng.Up(cfg); err != nil {
		log.Fatalf("engine Up: %v", err)
	}
	defer eng.Down()

	dialer, err := eng.Dialer()
	if err != nil {
		log.Fatalf("dialer: %v", err)
	}

	// Wait for a handshake (liveness source of truth — not a ping).
	fmt.Print("waiting for handshake")
	var handshook bool
	for i := 0; i < 30; i++ {
		st, _ := eng.Stats()
		if st.HandshakeAgeSec >= 0 {
			handshook = true
			fmt.Printf("\nhandshake OK (age=%ds, rx=%d tx=%d)\n", st.HandshakeAgeSec, st.RxBytes, st.TxBytes)
			break
		}
		fmt.Print(".")
		time.Sleep(1 * time.Second)
	}
	if !handshook {
		log.Fatal("\nNO HANDSHAKE after 30s — tunnel did not establish")
	}

	// The real test: make an HTTP request whose connections dial THROUGH the
	// tunnel, and read back the egress IP. Must be the node, not the host WAN.
	tunnelClient := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
	through := egressIP(tunnelClient.Do)
	fmt.Printf("egress THROUGH tunnel: %s\n", through)

	// Verdict.
	fmt.Println("----")
	switch {
	case through == "":
		log.Fatal("FAIL: handshake succeeded but NO traffic flowed through the tunnel (the classic 'handshake ≠ traffic' trap)")
	case through == direct:
		log.Fatalf("FAIL: egress through tunnel (%s) == direct egress (%s) — traffic did NOT take the tunnel", through, direct)
	case *expectEgress != "" && through != *expectEgress:
		log.Fatalf("FAIL: egress %s != expected node IP %s", through, *expectEgress)
	default:
		fmt.Printf("PASS: traffic egresses through the tunnel at %s (was %s direct)\n", through, direct)
	}

	// RAM working-set (NFR-2): the real constraint on 256MB hardware.
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("Go heap in use: %.1f MiB (Sys reserved %.1f MiB)\n",
		float64(m.HeapInuse)/(1<<20), float64(m.Sys)/(1<<20))
	fmt.Println("(RSS via /usr/bin/time -v or `cat /proc/self/status | grep VmRSS`)")
}

// egressIP fetches our public IP from a service that returns it as plain text.
// do is the request function so we can swap in a tunnel-dialing client.
func egressIP(do func(*http.Request) (*http.Response, error)) string {
	// Use several echo services; the first that answers wins. Plain-text IP.
	for _, url := range []string{"http://ifconfig.me/ip", "http://api.ipify.org", "http://icanhazip.com"} {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("User-Agent", "curl/8")
		resp, err := do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if ip := strings.TrimSpace(string(b)); net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}
