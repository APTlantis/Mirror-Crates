package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeIndexFile(t *testing.T, dir string, lines []string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(dir, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProcessIndexFile_IncludeYankedAndLimit(t *testing.T) {
	tmp := t.TempDir()
	idx := filepath.Join(tmp, "index", "s", "se", "serde")
	writeIndexFile(t, idx, []string{
		`{"name":"serde","vers":"1.0.0","cksum":"ab","yanked":false}`,
		`{"name":"serde","vers":"1.0.1","cksum":"cd","yanked":true}`,
	})

	out := filepath.Join(tmp, "out")

	// includeYanked=false -> only first
	limit := NewLimitCounter(10)
	ctrs := &counters{}
	if err := ProcessIndexFile(filepath.Join(tmp, "index"), idx, out, false, limit, "https://static.crates.io/crates", ctrs); err != nil && !errors.Is(err, ErrLimitReached) {
		t.Fatalf("ProcessIndexFile err: %v", err)
	}
	// Expect 1 sidecar
	dir := CrateDirFor("serde", out)
	if _, err := os.Stat(filepath.Join(dir, "serde-1.0.0.crate.json")); err != nil {
		t.Fatalf("expected sidecar for 1.0.0: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "serde-1.0.1.crate.json")); err == nil {
		t.Fatalf("did not expect sidecar for yanked 1.0.1")
	}

	// includeYanked=true with limit=1 -> only one file written
	limit2 := NewLimitCounter(1)
	ctrs2 := &counters{}
	if err := ProcessIndexFile(filepath.Join(tmp, "index"), idx, out, true, limit2, "https://static.crates.io/crates", ctrs2); err != nil && !errors.Is(err, ErrLimitReached) {
		t.Fatalf("ProcessIndexFile err: %v", err)
	}
	// We should still only have two possible files, but ensure limit decremented to 0
	if limit2.Remaining() != 0 {
		t.Fatalf("expected limit2==0, got %d", limit2.Remaining())
	}
}
