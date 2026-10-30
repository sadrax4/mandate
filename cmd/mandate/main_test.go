package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte(`# comment
PLAIN=sandbox            # trailing comment
QUOTED="ab #cd"
SINGLE='x y'
export EXPORTED=1
DUP=first
DUP=second
PRESET=from-file
`), 0o644)
	t.Setenv("PRESET", "from-env")
	for _, k := range []string{"PLAIN", "QUOTED", "SINGLE", "EXPORTED", "DUP"} {
		os.Unsetenv(k)
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	loadDotEnv(path)
	want := map[string]string{"PLAIN": "sandbox", "QUOTED": "ab #cd", "SINGLE": "x y", "EXPORTED": "1", "DUP": "second", "PRESET": "from-env"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
