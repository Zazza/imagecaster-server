package installer

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func extractZip(zipPath, targetDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	parent, base := filepath.Dir(targetDir), filepath.Base(targetDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	removeUnfinished(parent, base+".part-")
	tmpDir, err := os.MkdirTemp(parent, base+".part-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	for _, f := range r.File {
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		if err := writeZipEntry(tmpDir, parts[1], f); err != nil {
			return err
		}
	}

	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		if err := renameRetry(tmpDir, targetDir); err != nil {
			return fmt.Errorf("move to target: %w", err)
		}
		return nil
	}
	if err := mergeInto(tmpDir, targetDir); err != nil {
		return fmt.Errorf("move to target: %w", err)
	}
	return nil
}

func removeUnfinished(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("[install] list %s: %v", dir, err)
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			log.Printf("[install] remove unfinished extract %s: %v", e.Name(), err)
		}
	}
}

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

func extractZipFlat(zipPath, targetDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	root, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}
	for _, f := range r.File {
		if err := writeZipEntry(root, f.Name, f); err != nil {
			return err
		}
	}
	return nil
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
