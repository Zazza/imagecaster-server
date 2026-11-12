package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"imagecaster-server/installer"
)

type fetchEvents struct {
	progress func(done, total int64)
	status   func(string)
}

func (e fetchEvents) report(done, total int64) {
	if e.progress != nil {
		e.progress(done, total)
	}
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
		got, err := fileSHA256(dest)
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
	if err := fetchPart(ctx, src, part, want, ev); err != nil {
		return false, err
	}
	if err := os.Rename(part, dest); err != nil {
		return false, fmt.Errorf("rename file: %w", err)
	}
	removeQuiet(part + ".meta")
	return false, nil
}

func fetchPart(ctx context.Context, src, part, want string, ev fetchEvents) error {
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
	}
	resp, err := installer.HTTPGetFrom(src, have)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()

	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	switch {
	case have > 0 && resp.StatusCode == http.StatusPartialContent:
		if start, ok := contentRangeStart(resp.Header.Get("Content-Range")); !ok || start != have {
			removePart(part)
			return fmt.Errorf("download: server resumed at a wrong offset, partial file discarded; run the download again")
		}
		if !sameSource(part, resp.Header) {
			resp.Body.Close()
			removePart(part)
			ev.say("source file changed, downloading from the start")
			log.Printf("[download] %s: source changed since the partial download, starting over", filepath.Base(part))
			return fetchPart(ctx, src, part, want, ev)
		}
		flags = os.O_WRONLY | os.O_APPEND
		ev.say("resuming from " + installer.FormatBytes(have))
	case resp.StatusCode == http.StatusOK:
		have = 0
		saveValidator(part, resp.Header)
	case have > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		if total, ok := contentRangeTotal(resp.Header.Get("Content-Range")); ok && total == have {
			ev.report(have, have)
			return verifyPart(part, want)
		}
		removePart(part)
		return fmt.Errorf("download failed: status %d, partial file discarded; run the download again", resp.StatusCode)
	default:
		return fmt.Errorf("download failed: status %d", resp.StatusCode)
	}

	h := sha256.New()
	if want != "" && have > 0 {
		if err := hashPrefix(h, part, have); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	total := resp.ContentLength
	if total > 0 {
		total += have
	}
	cw := &countingWriter{w: f, done: have, total: total, report: ev.report}
	var dst io.Writer = cw
	if want != "" {
		dst = io.MultiWriter(cw, h)
	}
	ev.report(have, total)
	_, copyErr := io.Copy(dst, resp.Body)
	if err := f.Sync(); err != nil && copyErr == nil {
		copyErr = err
	}
	if err := f.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("download cancelled at %s; run it again to resume", installer.FormatBytes(cw.done))
		}
		return fmt.Errorf("download interrupted at %s of %s: %v; run it again to resume", installer.FormatBytes(cw.done), installer.FormatBytes(total), copyErr)
	}
	if total > 0 && cw.done != total {
		return fmt.Errorf("download incomplete: %s of %s; run it again to resume", installer.FormatBytes(cw.done), installer.FormatBytes(total))
	}
	if want == "" {
		return nil
	}
	return checkSum(part, want, hex.EncodeToString(h.Sum(nil)))
}

func verifyPart(part, want string) error {
	if want == "" {
		return nil
	}
	got, err := fileSHA256(part)
	if err != nil {
		return err
	}
	return checkSum(part, want, got)
}

func checkSum(part, want, got string) error {
	if got == want {
		return nil
	}
	removePart(part)
	return fmt.Errorf("sha256 mismatch: expected %s, got %s; the downloaded file was deleted", want, got)
}

func hashPrefix(h hash.Hash, path string, n int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read partial file: %w", err)
	}
	defer f.Close()
	if _, err := io.CopyN(h, f, n); err != nil {
		return fmt.Errorf("read partial file: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func contentRangeStart(v string) (int64, bool) {
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(start, 10, 64)
	return n, err == nil
}

func contentRangeTotal(v string) (int64, bool) {
	_, total, ok := strings.Cut(v, "/")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	return n, err == nil
}

type countingWriter struct {
	w      io.Writer
	done   int64
	total  int64
	report func(done, total int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.done += int64(n)
	if c.report != nil && n > 0 {
		c.report(c.done, c.total)
	}
	return n, err
}

func removeQuiet(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[download] remove %s: %v", path, err)
	}
}

func removePart(part string) {
	removeQuiet(part)
	removeQuiet(part + ".meta")
}

func strongETag(h http.Header) string {
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		return "etag " + et
	}
	return ""
}

func lastModified(h http.Header) string {
	if lm := h.Get("Last-Modified"); lm != "" {
		return "last-modified " + lm
	}
	return ""
}

func saveValidator(part string, h http.Header) {
	v := strongETag(h)
	if v == "" {
		v = lastModified(h)
	}
	if v == "" {
		removeQuiet(part + ".meta")
		return
	}
	if err := os.WriteFile(part+".meta", []byte(v), 0o644); err != nil {
		log.Printf("[download] save %s.meta: %v", filepath.Base(part), err)
	}
}

func sameSource(part string, h http.Header) bool {
	saved, err := os.ReadFile(part + ".meta")
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[download] read %s.meta: %v", filepath.Base(part), err)
		}
		return true
	}
	if strings.HasPrefix(string(saved), "etag ") {
		return strongETag(h) == string(saved)
	}
	return lastModified(h) == string(saved)
}
