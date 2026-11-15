package edit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
)

// GetJSON reads a dot-separated key path from a JSON or JSONC file, for the
// tests to look at what an edit wrote (the package itself only writes).
func GetJSON(path, keyPath string) (string, bool) {
	raw, err := Read(path)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	r := gjson.GetBytes(jsonc.ToJSON(raw), keyPath)
	if !r.Exists() {
		return "", false
	}
	return r.String(), true
}

func tmpFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
