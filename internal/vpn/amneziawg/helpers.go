package amneziawg

import (
	"fmt"
	"net"
	"time"
)

// nowUnix returns the current Unix time in seconds. Wrapped so stats math has a
// single source of "now".
func nowUnix() int64 { return time.Now().Unix() }

// resolveEndpoint turns "host:port" into "ip:port" for the UAPI endpoint key.
// AmneziaWG's UAPI wants a resolved address. If host is already an IP this is a
// no-op; otherwise it does a DNS lookup and uses the first address.
func resolveEndpoint(endpoint string) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("amneziawg: bad endpoint %q: %w", endpoint, err)
	}
	if ip := net.ParseIP(host); ip != nil {
		return endpoint, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("amneziawg: resolve endpoint host %q: %w", host, err)
	}
	return net.JoinHostPort(ips[0].String(), port), nil
}
