package dl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"imagecaster-server/fsutil"
)

type Options struct {
	SHA256     string
	Size       int64
	Client     *http.Client
	Header     http.Header
	Stall      time.Duration
	OnProgress func(done, total int64)
	OnStatus   func(msg string)
}

func (o Options) report(done, total int64) {
	if o.OnProgress != nil {
		o.OnProgress(done, total)
	}
}

func (o Options) say(msg string) {
	if o.OnStatus != nil {
		o.OnStatus(msg)
	}
}

type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("download failed: status %d", e.Code)
}

var ErrChecksum = errors.New("sha256 mismatch")

type ChecksumError struct {
	Want, Got string
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("sha256 mismatch: expected %s, got %s; the downloaded file was deleted", e.Want, e.Got)
}

func (e *ChecksumError) Is(target error) bool {
	return target == ErrChecksum
}

func Resume(ctx context.Context, url, part string, o Options) error {
	o.SHA256 = strings.ToLower(o.SHA256)
	have, err := partSize(part)
	if err != nil {
		return err
	}
	if o.Size > 0 && have >= o.Size {
		if have == o.Size && VerifyFile(part, o.SHA256) == nil {
			o.say("already downloaded")
			o.report(have, have)
			return nil
		}
		if err := Remove(part); err != nil {
			return err
		}
		have = 0
	}
	return fetch(ctx, url, part, have, o)
}

func fetch(ctx context.Context, url, part string, have int64, o Options) error {
	resp, err := GetFrom(ctx, url, have, o)
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
			removeQuiet(part)
			removeQuiet(metaPath(part))
			return fmt.Errorf("download: server resumed at a wrong offset, partial file discarded; run the download again")
		}
		if !sameSource(part, resp.Header) {
			resp.Body.Close()
			if err := Remove(part); err != nil {
				return err
			}
			o.say("source file changed, downloading from the start")
			log.Printf("[download] %s: source changed since the partial download, starting over", filepath.Base(part))
			return fetch(ctx, url, part, 0, o)
		}
		flags = os.O_WRONLY | os.O_APPEND
		o.say("resuming from " + FormatBytes(have))
	case resp.StatusCode == http.StatusOK:
		have = 0
		saveValidator(part, resp.Header)
	case have > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		if total, ok := contentRangeTotal(resp.Header.Get("Content-Range")); ok && total == have {
			o.report(have, have)
			return verifyOrRemove(part, o.SHA256)
		}
		if _, ok := contentRangeTotal(resp.Header.Get("Content-Range")); !ok && o.SHA256 != "" && VerifyFile(part, o.SHA256) == nil {
			o.report(have, have)
			return nil
		}
		removeQuiet(part)
		removeQuiet(metaPath(part))
		return fmt.Errorf("download failed: status %d, partial file discarded; run the download again", resp.StatusCode)
	default:
		return &StatusError{Code: resp.StatusCode}
	}

	h := sha256.New()
	if o.SHA256 != "" && have > 0 {
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
	} else if o.Size > 0 {
		total = o.Size
	}
	cw := &countingWriter{w: f, done: have, total: total, report: o.report}
	var dst io.Writer = cw
	if o.SHA256 != "" {
		dst = io.MultiWriter(cw, h)
	}
	o.report(have, total)
	_, copyErr := io.Copy(dst, resp.Body)
	if err := f.Sync(); err != nil && copyErr == nil {
		copyErr = err
	}
	if err := f.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("download cancelled at %s; run it again to resume: %w", FormatBytes(cw.done), ctx.Err())
		}
		return fmt.Errorf("download interrupted at %s of %s: %w; run it again to resume", FormatBytes(cw.done), FormatBytes(total), copyErr)
	}
	if total > 0 && cw.done != total {
		return fmt.Errorf("download incomplete: %s of %s; run it again to resume", FormatBytes(cw.done), FormatBytes(total))
	}
	if o.SHA256 == "" {
		return nil
	}
	return checkSum(part, o.SHA256, hex.EncodeToString(h.Sum(nil)))
}

func partSize(part string) (int64, error) {
	st, err := os.Stat(part)
	if err == nil {
		return st.Size(), nil
	}
	if os.IsNotExist(err) {
		return 0, nil
	}
	return 0, err
}

func Commit(part, dest string) error {
	if err := fsutil.Rename(part, dest); err != nil {
		return err
	}
	removeQuiet(metaPath(part))
	return nil
}

func Remove(part string) error {
	if err := os.Remove(part); err != nil && !os.IsNotExist(err) {
		return err
	}
	removeQuiet(metaPath(part))
	return nil
}

func FileSHA256(path string) (string, error) {
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

func VerifyFile(path, want string) error {
	if want == "" {
		return nil
	}
	got, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return &ChecksumError{Want: strings.ToLower(want), Got: got}
	}
	return nil
}

func verifyOrRemove(part, want string) error {
	if want == "" {
		return nil
	}
	got, err := FileSHA256(part)
	if err != nil {
		return err
	}
	return checkSum(part, want, got)
}

func checkSum(part, want, got string) error {
	if got == want {
		return nil
	}
	removeQuiet(part)
	removeQuiet(metaPath(part))
	return &ChecksumError{Want: want, Got: got}
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
	if n > 0 {
		c.report(c.done, c.total)
	}
	return n, err
}

func removeQuiet(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[download] remove %s: %v", path, err)
	}
}

func metaPath(part string) string {
	return part + ".meta"
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
		removeQuiet(metaPath(part))
		return
	}
	if err := os.WriteFile(metaPath(part), []byte(v), 0o644); err != nil {
		log.Printf("[download] save %s: %v", filepath.Base(metaPath(part)), err)
	}
}

func sameSource(part string, h http.Header) bool {
	saved, err := os.ReadFile(metaPath(part))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[download] read %s: %v", filepath.Base(metaPath(part)), err)
		}
		return true
	}
	if strings.HasPrefix(string(saved), "etag ") {
		return strongETag(h) == string(saved)
	}
	return lastModified(h) == string(saved)
}

func FormatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
