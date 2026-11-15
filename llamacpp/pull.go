package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"imagecaster-server/dl"
	"imagecaster-server/fsutil"
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
	Stall   time.Duration
	BaseURL func(host string) string
}

func (p *Puller) base(n Name) string {
	if p.BaseURL != nil {
		return strings.TrimSuffix(p.BaseURL(n.Host), "/")
	}
	return "https://" + n.Host
}

func (p *Puller) getManifest(ctx context.Context, url string) (*http.Response, error) {
	return dl.GetFrom(ctx, url, 0, dl.Options{Client: p.HTTP, Stall: p.Stall, Header: http.Header{"Accept": {MediaManifest}}})
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
	resp, err := p.getManifest(ctx, repo+"/manifests/"+n.Tag)
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
	if err := fsutil.WriteFileAtomic(dest, raw, 0o644); err != nil {
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
	want, _ := digestHex(l.Digest)
	status := "pulling " + want[:12]
	if st, err := os.Stat(dest); err == nil && (l.Size <= 0 || st.Size() == l.Size) {
		progress(Progress{Status: status, Digest: l.Digest, Total: l.Size, Completed: l.Size})
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + "-partial"
	var last time.Time
	err = dl.Resume(ctx, repo+"/blobs/"+l.Digest, part, dl.Options{
		SHA256: want,
		Size:   l.Size,
		Client: p.HTTP,
		Stall:  p.Stall,
		OnProgress: func(done, total int64) {
			if time.Since(last) > 500*time.Millisecond {
				last = time.Now()
				progress(Progress{Status: status, Digest: l.Digest, Total: l.Size, Completed: done})
			}
		},
	})
	var ce *dl.ChecksumError
	if errors.As(err, &ce) {
		return fmt.Errorf("%s: %w (got sha256:%s)", status, ErrDigest, ce.Got)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", status, err)
	}
	progress(Progress{Status: status, Digest: l.Digest, Total: l.Size, Completed: l.Size})
	return dl.Commit(part, dest)
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
	return fmt.Sprintf("%s: %d%% %s/%s", p.Status, pct, dl.FormatBytes(p.Completed), dl.FormatBytes(p.Total))
}
