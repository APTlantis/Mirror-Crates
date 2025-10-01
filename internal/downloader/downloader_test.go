package downloader

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCrateDirFor(t *testing.T) {
	out := t.TempDir()
	// Short names (<=3)
	if got := crateDirFor("ab", out); got != filepath.Join(out, "ab") {
		t.Fatalf("crateDirFor short: got %q", got)
	}
	if got := crateDirFor("abc", out); got != filepath.Join(out, "abc") {
		t.Fatalf("crateDirFor 3-len: got %q", got)
	}
	// Normal name
	if got := crateDirFor("serde", out); got != filepath.Join(out, "s", "er") {
		t.Fatalf("crateDirFor serde: got %q", got)
	}
	// Starts with digit 1..3 -> first dir is first char
	if got := crateDirFor("1serde", out); got != filepath.Join(out, "1", "se") {
		t.Fatalf("crateDirFor 1serde: got %q", got)
	}
}

func TestSanitizeName(t *testing.T) {
	u := "https://static.crates.io/crates/serde/serde-1.0.0.crate"
	if got := sanitizeName(u); got != "serde-1.0.0.crate" {
		t.Fatalf("sanitizeName: got %q", got)
	}
	u2 := "https://example.com/x/file?foo=1&bar=2"
	got := sanitizeName(u2)
	if !strings.Contains(got, "_") {
		t.Fatalf("sanitizeName should replace special chars: %q", got)
	}
}

func TestHeaderPathFor(t *testing.T) {
	base := "serde-1.0.0.crate"
	hp := headerPathFor("https://static.crates.io/crates/serde/serde-1.0.0.crate", base)
	if !strings.HasPrefix(hp, "static.crates.io") || !strings.HasSuffix(hp, base) {
		t.Fatalf("headerPathFor unexpected: %q", hp)
	}
}

func TestVerifyFile(t *testing.T) {
	d := &Downloader{checksums: map[string]string{}}
	f := filepath.Join(t.TempDir(), "x.bin")
	content := []byte("hello world\n")
	if err := os.WriteFile(f, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	sum := sha256.Sum256(content)
	url := "https://example.com/x.bin"
	d.checksums[url] = hex.EncodeToString(sum[:])
	ok, got := d.verifyFile(f, url)
	if !ok {
		t.Fatalf("verifyFile should pass, got sum=%s", got)
	}
	d.checksums[url] = strings.Repeat("0", 64)
	ok, _ = d.verifyFile(f, url)
	if ok {
		t.Fatalf("verifyFile should fail with wrong checksum")
	}
}

func TestBundlerRotation(t *testing.T) {
	// Create two small files
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.txt")
	b := filepath.Join(tmp, "b.txt")
	if err := os.WriteFile(a, []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(strings.Repeat("B", 1024)), 0o644); err != nil {
		t.Fatal(err)
	}

	bundlesOut := filepath.Join(tmp, "bundles")
	// targetGB=0 rotates on every add
	bndl, err := NewBundler(true, bundlesOut, 0)
	if err != nil {
		t.Fatalf("NewBundler: %v", err)
	}
	defer bndl.Close()

	if err := bndl.AddFile(a, "a.txt"); err != nil {
		t.Fatalf("AddFile a: %v", err)
	}
	if err := bndl.AddFile(b, "b.txt"); err != nil {
		t.Fatalf("AddFile b: %v", err)
	}
	_ = bndl.Close()
	// Allow FS to flush on slow runners
	time.Sleep(50 * time.Millisecond)

	// Expect at least two bundle files
	entries, err := os.ReadDir(bundlesOut)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected >=2 bundle files, got %d", len(entries))
	}
	runtime.KeepAlive(bndl)
}

func TestReadCratesFromIndex_FlagsAndLimit(t *testing.T) {
	tmp := t.TempDir()
	// Synthesize a tiny index
	idxFile := filepath.Join(tmp, "s", "se", "serde")
	if err := os.MkdirAll(filepath.Dir(idxFile), 0o755); err != nil {
		t.Fatal(err)
	}
	data := ""
	data += `{"name":"serde","vers":"1.0.0","cksum":"` + strings.Repeat("a", 64) + `","yanked":false}` + "\n"
	data += `{"name":"serde","vers":"1.0.1","cksum":"` + strings.Repeat("b", 64) + `","yanked":true}` + "\n"
	if err := os.WriteFile(idxFile, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	// includeYanked=false
	urls, sums, err := ReadCratesFromIndex(tmp, "https://static.crates.io/crates", false, 0)
	if err != nil {
		t.Fatalf("ReadCratesFromIndex err: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("expect 1 url, got %d", len(urls))
	}
	if len(sums) != 1 {
		t.Fatalf("expect 1 checksum, got %d", len(sums))
	}

	// includeYanked=true, limit=1
	urls2, _, err := ReadCratesFromIndex(tmp, "https://static.crates.io/crates", true, 1)
	if err != nil {
		t.Fatalf("ReadCratesFromIndex err: %v", err)
	}
	if got := len(urls2); got != 1 {
		t.Fatalf("limit not applied, got %d", got)
	}
}
