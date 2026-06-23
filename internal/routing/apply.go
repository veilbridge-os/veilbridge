package routing

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Apply renders rules and feeds the script to `nft -f -`. It is idempotent: the
// script (via Render) deletes and recreates VeilBridge's table wholesale.
// Requires nft on PATH and CAP_NET_ADMIN (root).
func (g Generator) Apply(ctx context.Context, rules []core.RouteRule) error {
	script, err := g.Render(rules)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("routing: nft apply failed: %w: %s", err, stderr.String())
	}
	return nil
}
