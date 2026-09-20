// Package architecture_test is the guard D-33 asks for: with the second
// platform gone, nothing but discipline keeps OS specifics from leaking out of
// the adapter, and discipline does not survive a year of commits. This test
// is that discipline, written down and executable.
//
// It is a text scan, deliberately. A type-level rule would not catch a string
// "uci set …" handed to a runner, and that is exactly the shape a leak takes.
package architecture_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// osSpecific are the tokens that mean "this code knows which operating system
// it is on". The word boundaries matter: "uci" would otherwise match
// "producing", and a guard that cries wolf gets deleted.
var osSpecific = []*regexp.Regexp{
	regexp.MustCompile(`\buci\b`),
	regexp.MustCompile(`\bubus\b`),
	regexp.MustCompile(`\bnft(ables)?\b`),
	regexp.MustCompile(`\bprocd\b`),
	regexp.MustCompile(`\bopkg\b`),
	regexp.MustCompile(`/etc/config\b`),
	regexp.MustCompile(`\bos/exec\b`),
}

// allowed are the directories that are permitted to know an OS. The adapter is
// the obvious one; the other two are the Linux implementation libraries the
// adapter composes (the nftables renderer and the tunnel engines), which is a
// narrower claim than "the adapter package only" and the true one. Keeping
// the list short is the point: every entry is a place the boundary does not
// protect, and adding one has to be a visible act.
var allowed = []string{
	"internal/adapters",
	"internal/routing",
	"internal/vpn",
	"internal/awgnetstack",
}

// guarded are the layers that must stay OS-agnostic: the domain model, the
// HTTP surface, the config store, and the daemon's own wiring. If any of them
// learns what uci is, the seam that makes this codebase testable off-device is
// already gone.
var guarded = []string{
	"internal/core",
	"internal/api",
	"internal/config",
	"internal/buildinfo",
	"cmd",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	// Prove we found the repo and not some parent: the guard must fail loudly
	// rather than scan an empty tree and report success.
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod: %v", root, err)
	}
	return root
}

// scanTree is the guard itself, separated from the assertion so it can be run
// against a tree with a known leak. Without that separation, disabling the
// reporting would go unnoticed: a scanner that reports nothing looks exactly
// like a clean repository.
func scanTree(root string, dirs []string) (findings []string, scanned int, err error) {
	for _, dir := range dirs {
		walkErr := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Production code only. A test in core may legitimately name uci:
			// core/apply_test.go feeds `errors.New("uci: invalid value")` to a
			// fake applier to prove the transaction reports what the adapter
			// told it. That is the seam working, not leaking - the string is
			// data crossing the boundary, and core never acts on it.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			rel, _ := filepath.Rel(root, path)
			for _, line := range strings.Split(string(body), "\n") {
				// A comment may name uci: explaining why core does not know
				// about it is the opposite of a leak.
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				for _, re := range osSpecific {
					if re.MatchString(line) {
						findings = append(findings, rel+": "+re.String()+": "+strings.TrimSpace(line))
					}
				}
			}
			return nil
		})
		if walkErr != nil {
			return findings, scanned, walkErr
		}
	}
	return findings, scanned, nil
}

func TestOSSpecificsStayBehindTheAdapter(t *testing.T) {
	root := repoRoot(t)
	findings, scanned, err := scanTree(root, guarded)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, f := range findings {
		t.Errorf("OS-specific token outside the adapter (D-33): %s", f)
	}
	// A guard that scanned nothing proves nothing - the exact failure mode of
	// a path that quietly stopped matching after a move.
	if scanned < 10 {
		t.Fatalf("only %d files scanned; the guard is looking in the wrong place", scanned)
	}
}

// The repository is clean, which means a broken scanner and a healthy
// codebase produce the same silence. This plants a leak and insists the
// scanner sees it, so the guard cannot rot into a no-op.
func TestGuardReportsAPlantedLeak(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "internal", "core")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	clean := "package core\n\nfunc Clean() string { return \"nothing to see\" }\n"
	leak := "package core\n\nimport \"os/exec\"\n\nfunc Leak() { _ = exec.Command(\"uci\", \"commit\") }\n"
	for name, body := range map[string]string{
		"clean.go": clean,
		"leak.go":  leak,
		// The same leak in a _test.go file must stay invisible, or the
		// exemption above is untested and the next person widens it by
		// accident.
		"decoy_test.go": leak,
	} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	findings, scanned, err := scanTree(root, []string{"internal/core"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scanned != 2 {
		t.Errorf("scanned %d files, want 2 (the _test.go file must be skipped)", scanned)
	}
	if len(findings) == 0 {
		t.Fatal("the planted leak went unreported: the guard is a no-op")
	}
	for _, f := range findings {
		if strings.Contains(f, "decoy_test.go") {
			t.Errorf("a _test.go file was reported: %s", f)
		}
		if strings.Contains(f, "clean.go") {
			t.Errorf("a clean file was reported: %s", f)
		}
	}
}

// The allow-list must describe directories that exist. A stale entry is a hole
// nobody notices: it silently permits nothing, until a real package moves into
// that path.
func TestAllowListIsNotStale(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range allowed {
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			t.Errorf("allow-list entry %q does not exist: %v", dir, err)
		}
	}
}

// The guard has to be able to fail. This proves the scanner actually matches
// what it claims to match, so a future refactor cannot turn it into a no-op
// that passes on an empty regex list.
func TestGuardDetectsALeak(t *testing.T) {
	leaks := []string{
		`out, _ := exec.Command("uci", "show").Output()`,
		"\tclient := ubus.New()",
		`script := "nft -f -"`,
		`os.ReadFile("/etc/config/network")`,
		`import "os/exec"`,
		`_ = "opkg install kmod-tun"`,
	}
	for _, line := range leaks {
		matched := false
		for _, re := range osSpecific {
			if re.MatchString(line) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("the guard would not notice this leak: %s", line)
		}
	}

	// And it must not fire on ordinary prose or identifiers that merely
	// contain the letters: a noisy guard is a disabled guard.
	innocent := []string{
		"// producing a value",
		"func reproduce() {}",
		"nftIsNotHereJustASubstring := 1",
		"conf := loadConfiguration()",
	}
	for _, line := range innocent {
		for _, re := range osSpecific {
			if re.MatchString(line) {
				t.Errorf("false positive: %q matched %s", line, re.String())
			}
		}
	}
}
