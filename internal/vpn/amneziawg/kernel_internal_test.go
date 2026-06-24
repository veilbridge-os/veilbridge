package amneziawg

import "testing"

// TestKernelEngineDialerNil: the kernel engine forwards transparently via
// nftables, so it must return a nil Dialer (the probe/system layer relies on
// this to distinguish kernel from userspace egress). No root needed.
func TestKernelEngineDialerNil(t *testing.T) {
	e := NewKernelEngine()
	d, err := e.Dialer()
	if err != nil {
		t.Fatalf("Dialer() error: %v", err)
	}
	if d != nil {
		t.Errorf("kernel engine Dialer() = %v, want nil", d)
	}
}

// TestKernelEngineStatsDown: Stats on a down engine reports never-handshaked
// (-1) and an error, mirroring netstackEngine's contract.
func TestKernelEngineStatsDown(t *testing.T) {
	e := NewKernelEngine()
	st, err := e.Stats()
	if err == nil {
		t.Error("Stats() on down engine should error")
	}
	if st.HandshakeAgeSec != -1 {
		t.Errorf("down HandshakeAgeSec = %d, want -1", st.HandshakeAgeSec)
	}
}

// TestKernelEngineDownIdempotent: Down on a never-upped engine is a no-op.
func TestKernelEngineDownIdempotent(t *testing.T) {
	if err := NewKernelEngine().Down(); err != nil {
		t.Errorf("Down() on fresh engine: %v", err)
	}
}
