package vdj

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

// compareGolden compares data against a golden file, rewriting it under
// -update. The golden carries a trailing hash line so a hand edit is
// detectable: a generated contract that someone patched by hand is no longer a
// contract, it is a wish.
func compareGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)

	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(path, appendHash(data), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}

	golden, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run with -update to create)", path, err)
	}
	want, _ := splitHash(golden)
	if string(data) != string(want) {
		t.Errorf("%s is out of date; regenerate with:\n"+
			"    go test ./pkg/dmn/vdj/ -run TestSchemaGolden -update\n"+
			"first difference at %s", name, firstDifference(string(want), string(data)))
	}
}

// firstDifference locates the first differing line, so the failure names the
// change rather than printing two thousand lines of JSON.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) && i < len(g); i++ {
		if w[i] != g[i] {
			return "line " + itoa(i+1) + ":\n  want: " + w[i] + "\n  got:  " + g[i]
		}
	}
	return "end of file (" + itoa(len(w)) + " want lines, " + itoa(len(g)) + " got lines)"
}

func appendHash(data []byte) []byte {
	sum := sha256.Sum256(data)
	return append(data, []byte("\n// golden-hash: "+hex.EncodeToString(sum[:])+"\n")...)
}

func splitHash(content []byte) (data []byte, hash string) {
	const marker = "\n// golden-hash: "
	s := string(content)
	i := strings.LastIndex(s, marker)
	if i < 0 {
		return content, ""
	}
	return []byte(s[:i]), strings.TrimSpace(s[i+len(marker):])
}

// TestGoldensNotHandEdited verifies every golden's hash still matches its
// content. The published schema is served from this file, so a hand edit would
// ship a contract no generator produced.
func TestGoldensNotHandEdited(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("reading testdata: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join("testdata", entry.Name()))
		if err != nil {
			t.Errorf("reading %s: %v", entry.Name(), err)
			continue
		}
		data, stored := splitHash(content)
		if stored == "" {
			t.Errorf("%s carries no golden-hash line; it may have been hand-edited", entry.Name())
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != stored {
			t.Errorf("%s: hash mismatch, the file has been edited by hand\n  stored:   %s\n  computed: %s",
				entry.Name(), stored, got)
		}
	}
}
