package fsutil

import (
	"os"
	"runtime"
	"time"
)

var (
	renameAttempts = 20
	renameDelay    = 250 * time.Millisecond
	rename         = os.Rename
)

func Rename(src, dst string) error {
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = renameAttempts
	}
	return renameWithRetry(src, dst, attempts)
}

func renameWithRetry(src, dst string, attempts int) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(renameDelay)
		}
		if err = rename(src, dst); err == nil {
			return nil
		}
	}
	return err
}

func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func ReplaceFile(path string, data []byte, perm os.FileMode) error {
	if st, err := os.Stat(path); err == nil {
		perm = st.Mode().Perm()
	}
	return WriteFileAtomic(path, data, perm)
}

func FileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func DirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
