package llamacpp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var AllowedHosts = map[string]bool{"registry.ollama.ai": true, "hf.co": true, "huggingface.co": true}

type Progress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
}

type Puller struct {
	Store   *Store
	HTTP    *http.Client
	BaseURL func(host string) string
}

func (p *Puller) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Timeout: 0}
}

func (p *Puller) base(n Name) string {
	if p.BaseURL != nil {
		return strings.TrimSuffix(p.BaseURL(n.Host), "/")
	}
	return "https://" + n.Host
}

func (p *Puller) get(ctx context.Context, url string, offset int64, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	return p.client().Do(req)
}

func (p *Puller) Pull(ctx context.Context, name string, progress func(Progress)) error {
	if progress == nil {
		progress = func(Progress) {}
	}
	n, err := ParseName(name)
	if err != nil {
		return err
	}
	if !AllowedHosts[strings.ToLower(n.Host)] {
		return fmt.Errorf("%w: registry %s is not allowed (registry.ollama.ai, hf.co)", ErrBadName, n.Host)
	}
	progress(Progress{Status: "pulling manifest"})
	repo := p.base(n) + "/v2/" + n.Namespace + "/" + n.Model
	resp, err := p.get(ctx, repo+"/manifests/"+n.Tag, 0, MediaManifest)
	if err != nil {
		return fmt.Errorf("pull %s: %w", n, err)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("pull %s: %w", n, err)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: %s (registry answered %d)", ErrNotFound, n, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull %s: manifest HTTP %d", n, resp.StatusCode)
	}
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return fmt.Errorf("pull %s: manifest: %w", n, err)
	}
	if _, ok := digestHex(mf.Config.Digest); !ok {
		return fmt.Errorf("pull %s: manifest has no config", n)
	}
	hasModel := false
	for _, l := range mf.Layers {
		if _, ok := digestHex(l.Digest); !ok {
			return fmt.Errorf("pull %s: bad layer digest %q", n, l.Digest)
		}
		hasModel = hasModel || l.MediaType == MediaModel
	}
	if !hasModel {
		return fmt.Errorf("pull %s: manifest has no model layer", n)
	}
	if err := p.fetchBlob(ctx, repo, mf.Config, progress); err != nil {
		return err
	}
	if b, err := p.Store.readBlob(mf.Config.Digest, 1<<20); err == nil {
		var cfg ModelConfig
		if json.Unmarshal(b, &cfg) == nil {
			if err := (Model{Name: n, Config: cfg, ModelPath: "-"}).Supported(); err != nil {
				return err
			}
		}
	}
	for _, l := range mf.Layers {
		if err := p.fetchBlob(ctx, repo, l, progress); err != nil {
			return err
		}
	}
	progress(Progress{Status: "writing manifest"})
	dest := filepath.Join(p.Store.manifestsDir(), n.manifestRel())
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	progress(Progress{Status: "success"})
	return nil
}

var ErrDigest = errors.New("digest mismatch")

func (p *Puller) fetchBlob(ctx context.Context, repo string, l Layer, progress func(Progress)) error {
	dest, err := p.Store.BlobPath(l.Digest)
	if err != nil {
		return err
	}
	status := "pulling " + strings.TrimPrefix(l.Digest, "sha256:")[:12]
	if st, err := os.Stat(dest); err == nil && (l.Size <= 0 || st.Size() == l.Size) {
		progress(Progress{Status: status, Digest: l.Digest, Total: l.Size, Completed: l.Size})
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + "-partial"
	h := sha256.New()
	var have int64
	if f, err := os.Open(part); err == nil {
		have, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if l.Size > 0 && have > l.Size {
		if err := os.Remove(part); err != nil {
			return err
		}
		have = 0
		h.Reset()
	}
	if l.Size <= 0 || have < l.Size {
		resp, err := p.get(ctx, repo+"/blobs/"+l.Digest, have, "")
		if err != nil {
			return fmt.Errorf("%s: %w", status, err)
		}
		defer resp.Body.Close()
		flags := os.O_CREATE | os.O_WRONLY
		switch {
		case have > 0 && resp.StatusCode == http.StatusPartialContent:
			flags |= os.O_APPEND
		case resp.StatusCode == http.StatusOK:
			flags |= os.O_TRUNC
			have = 0
			h.Reset()
		default:
			return fmt.Errorf("%s: HTTP %d", status, resp.StatusCode)
		}
		f, err := os.OpenFile(part, flags, 0o644)
		if err != nil {
			return err
		}
		pw := &pullWriter{total: l.Size, done: have, status: status, digest: l.Digest, progress: progress}
		_, cerr := io.Copy(io.MultiWriter(f, h, pw), resp.Body)
		if err := f.Close(); err != nil && cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return fmt.Errorf("%s: %w", status, cerr)
		}
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != strings.ToLower(l.Digest) {
		os.Remove(part)
		return fmt.Errorf("%s: %w (got %s)", status, ErrDigest, got)
	}
	progress(Progress{Status: status, Digest: l.Digest, Total: l.Size, Completed: l.Size})
	return os.Rename(part, dest)
}

type pullWriter struct {
	total, done int64
	status      string
	digest      string
	progress    func(Progress)
	last        time.Time
}

func (w *pullWriter) Write(b []byte) (int, error) {
	w.done += int64(len(b))
	if time.Since(w.last) > 500*time.Millisecond {
		w.last = time.Now()
		w.progress(Progress{Status: w.status, Digest: w.digest, Total: w.total, Completed: w.done})
	}
	return len(b), nil
}

func (f *Facade) Pull(ctx context.Context, name string, progress func(Progress)) error {
	if f.cfg.Puller == nil {
		return errors.New("pull is not configured")
	}
	if err := f.cfg.Puller.Pull(ctx, name, progress); err != nil {
		return err
	}
	return f.Refresh(context.WithoutCancel(ctx))
}

func (f *Facade) handlePull(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	stream := true
	if s, ok := body["stream"].(bool); ok {
		stream = s
	}
	name := modelField(body)
	if !stream {
		if err := f.Pull(r.Context(), name, nil); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, Progress{Status: "success"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	err = f.Pull(r.Context(), name, func(p Progress) {
		enc.Encode(p)
		if fl != nil {
			fl.Flush()
		}
	})
	if err != nil {
		enc.Encode(map[string]string{"error": err.Error()})
	}
}

func FormatProgress(p Progress) string {
	if p.Total <= 0 {
		return p.Status
	}
	pct := p.Completed * 100 / p.Total
	return fmt.Sprintf("%s: %d%% %s/%s", p.Status, pct, gb(p.Completed), gb(p.Total))
}

func gb(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d B", n)
}
