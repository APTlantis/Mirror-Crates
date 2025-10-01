package sidecar

import (
	"path/filepath"
	"testing"
)

func TestSidecarCrateDirFor(t *testing.T) {
	out := t.TempDir()
	if got := CrateDirFor("serde", out); got != filepath.Join(out, "s", "er") {
		t.Fatalf("CrateDirFor serde: got %q", got)
	}
	if got := CrateDirFor("ab", out); got != filepath.Join(out, "ab") {
		t.Fatalf("CrateDirFor short: got %q", got)
	}
}
