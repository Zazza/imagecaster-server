package installer

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func mergeInto(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		st, err := os.Stat(d)
		if err == nil && st.IsDir() && e.IsDir() {
			if err := mergeInto(s, d); err != nil {
				return err
			}
			continue
		}
		if err == nil {
			if err := os.RemoveAll(d); err != nil {
				return err
			}
		}
		if err := renameRetry(s, d); err != nil {
			return err
		}
	}
	return nil
}

func renameRetry(src, dst string) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return err
}

func safeJoin(root, name string) (string, error) {
	dest := filepath.Join(root, filepath.FromSlash(name))
	if dest != root && !strings.HasPrefix(dest, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("zip entry escapes target: %s", name)
	}
	return dest, nil
}

func writeZipEntry(root, name string, f *zip.File) error {
	dest, err := safeJoin(root, name)
	if err != nil {
		return err
	}
	if f.FileInfo().IsDir() {
		return os.MkdirAll(dest, 0o755)
	}
	if dest == root {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return extractZipEntry(f, dest)
}

func matchBinaryName(name, target string) bool {
	if name == target {
		return true
	}
	if runtime.GOOS == "windows" && name == target+".exe" {
		return true
	}
	return false
}

func extractZipEntry(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := f.Mode().Perm() | 0o600
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
