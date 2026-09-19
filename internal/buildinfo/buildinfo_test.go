package buildinfo

import (
	"strings"
	"testing"
)

// The release workflow greps the -version output for the tag. If String() ever
// stops echoing the injected version verbatim, the release would still publish
// a binary that lies about which build it is — so pin the contract here.
func TestStringContainsVersionVerbatim(t *testing.T) {
	info := Info{
		Version:  "v9.9.9",
		Revision: "a3c1b1e0123456789",
		Time:     "2026-09-19T08:00:00Z",
		Go:       "go1.26.4",
		Platform: "linux/arm64",
	}

	got := info.String()
	for _, want := range []string{
		"veilbridged", "v9.9.9", "a3c1b1e", "2026-09-19T08:00:00Z",
		"go1.26.4", "linux/arm64",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "a3c1b1e0123456789") {
		t.Errorf("String() = %q, want the revision abbreviated to 7 chars", got)
	}
}

func TestStringMarksDirtyTree(t *testing.T) {
	info := Info{Version: "devel", Revision: "abcdef1234", Dirty: true}
	if got := info.String(); !strings.Contains(got, "abcdef1-dirty") {
		t.Errorf("String() = %q, want it to mark the tree dirty", got)
	}
}

// A source build has no linker-injected version and must not claim a release.
func TestReadFallsBackToDevel(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	version = ""
	if got := Read().Version; got != "devel" {
		t.Errorf("Read().Version = %q, want %q", got, "devel")
	}

	version = "v1.2.3"
	if got := Read().Version; got != "v1.2.3" {
		t.Errorf("Read().Version = %q, want %q", got, "v1.2.3")
	}
}

// Without VCS stamps or a version, the line must still be usable in a bug
// report rather than collapsing into stray punctuation.
func TestStringWithoutStamps(t *testing.T) {
	got := Info{Version: "devel", Go: "go1.26.4", Platform: "linux/amd64"}.String()
	if want := "veilbridged devel go1.26.4 linux/amd64"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
