package models

import (
	"context"
	"encoding/hex"
	"fmt"
	"imagecaster-server/dl"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type fetchEvents struct {
	progress func(done, total int64)
	status   func(string)
}

func (e fetchEvents) say(msg string) {
	if e.status != nil {
		e.status(msg)
	}
}

func parseSHA256(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if len(s) != 64 {
		return "", fmt.Errorf("invalid sha256: want 64 hex characters, got %d", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("invalid sha256: not hex")
	}
	return s, nil
}

func (m *ModelManager) claim(dest string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = map[string]bool{}
	}
	if m.active[dest] {
		return false
	}
	m.active[dest] = true
	return true
}

func (m *ModelManager) release(dest string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, dest)
}

func (m *ModelManager) fetch(ctx context.Context, src, dir, filename, want string, ev fetchEvents) (bool, error) {
	dest := filepath.Join(dir, filename)
	if !m.claim(dest) {
		return false, fmt.Errorf("%s is already downloading", filename)
	}
	defer m.release(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create dir: %w", err)
	}
	if st, err := os.Stat(dest); err == nil && st.Mode().IsRegular() {
		if want == "" {
			return true, nil
		}
		ev.say("checking sha256 of " + filename)
		got, err := dl.FileSHA256(dest)
		if err != nil {
			return false, err
		}
		if got == want {
			return true, nil
		}
		log.Printf("[download] %s exists but sha256 is %s, expected %s: downloading again", filename, got, want)
	}
	if err := os.Remove(dest + ".downloading"); err != nil && !os.IsNotExist(err) {
		log.Printf("[download] remove old partial file: %v", err)
	}
	part := dest + ".part"
	if err := dl.Resume(ctx, src, part, dl.Options{SHA256: want, OnProgress: ev.progress, OnStatus: ev.status}); err != nil {
		return false, err
	}
	if err := dl.Commit(part, dest); err != nil {
		return false, fmt.Errorf("rename file: %w", err)
	}
	return false, nil
}
