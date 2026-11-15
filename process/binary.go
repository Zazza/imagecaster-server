package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func FindBinary(dataDir, name string) string {
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	candidate := filepath.Join(dataDir, "bin", file)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return ""
}
