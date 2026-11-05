package config

import (
	"path/filepath"
	"testing"
)

func TestLoadWithDirFirstStartKeepsDataDir(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "server-config.yaml")

	first, err := LoadWithDir(path, dataDir)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if first.DataDir != dataDir {
		t.Fatalf("first load data dir = %q, want %q", first.DataDir, dataDir)
	}

	second, err := LoadWithDir(path, dataDir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second.DataDir != dataDir {
		t.Fatalf("second load data dir = %q, want %q", second.DataDir, dataDir)
	}
}
