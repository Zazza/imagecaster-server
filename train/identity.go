package train

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"imagecaster-server/faceswap"
	"imagecaster-server/fsutil"
)

const minIdentityPhotos = 3

func (m *Manager) SetIdentityBuilder(b IdentityBuilder) {
	m.mu.Lock()
	m.ident = b
	m.mu.Unlock()
}

func (m *Manager) identityBuilder() IdentityBuilder {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ident
}

func photoPaths(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".jpg", ".png", ".webp":
			if !e.IsDir() {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out
}

func identitySkip(err error) string {
	switch {
	case errors.Is(err, faceswap.ErrTooFewFaces):
		return "too_few_faces"
	case errors.Is(err, faceswap.ErrUnavailable):
		return "face_models_missing"
	}
	return "failed"
}

func (m *Manager) buildIdentity(ctx context.Context, j *Job, datasets ...string) (*faceswap.Identity, error) {
	b := m.identityBuilder()
	if b == nil {
		return nil, nil
	}
	var ident *faceswap.Identity
	status := "skipped:face_models_missing"
	if b.Available() {
		var paths []string
		for _, d := range datasets {
			if d != "" {
				paths = append(paths, photoPaths(d)...)
			}
		}
		got, err := b.FromImages(ctx, paths)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			log.Printf("[train] job %s: face identity skipped: %v", j.ID, err)
			status = "skipped:" + identitySkip(err)
		} else {
			ident, status = &got, "done"
		}
	}
	m.mu.Lock()
	j.Identity = status
	m.persist(j)
	m.mu.Unlock()
	return ident, nil
}

func ReadIdentity(loraDir, name string) ([]float32, error) {
	if !nameRe.MatchString(name) || loraDir == "" {
		return nil, faceswap.ErrPersonNotFound
	}
	pf, err := readPersonFile(filepath.Join(loraDir, name+".person.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, faceswap.ErrPersonNotFound
	}
	if err != nil {
		return nil, err
	}
	if pf.Identity == nil || len(pf.Identity.Embedding) == 0 {
		return nil, faceswap.ErrNoIdentity
	}
	return pf.Identity.Embedding, nil
}

func (m *Manager) handleIdentity(w http.ResponseWriter, r *http.Request, name string) {
	if !nameRe.MatchString(name) {
		writeError(w, errf(http.StatusNotFound, "not_found", "person %q not found", name))
		return
	}
	if _, err := os.Stat(m.personPath(name)); err != nil {
		writeError(w, errf(http.StatusNotFound, "not_found", "person %q not found", name))
		return
	}
	b := m.identityBuilder()
	if b == nil || !b.Available() {
		writeError(w, errf(http.StatusServiceUnavailable, "face_models_missing", "face models or ONNX Runtime are missing on the server"))
		return
	}
	id, err := newID()
	if err != nil {
		writeError(w, errf(http.StatusInternalServerError, "internal", "%v", err))
		return
	}
	dir := filepath.Join(m.cfg.JobsDir, "identity-"+id)
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("[train] remove identity upload %s: %v", dir, err)
		}
	}()
	form, herr := m.readUpload(w, r, dir)
	if herr != nil {
		writeError(w, herr)
		return
	}
	if form.photos < minIdentityPhotos {
		writeError(w, errf(http.StatusBadRequest, "invalid_request", "need %d-%d photos, got %d", minIdentityPhotos, maxPhotos, form.photos))
		return
	}
	ident, err := b.FromImages(r.Context(), photoPaths(dir))
	if errors.Is(err, faceswap.ErrTooFewFaces) {
		writeError(w, errf(http.StatusBadRequest, "too_few_faces", "need at least %d photos with a clearly visible face: %v", faceswap.MinIdentityFaces, err))
		return
	}
	if err != nil {
		writeError(w, errf(http.StatusInternalServerError, identitySkip(err), "%v", err))
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pf, err := readPersonFile(m.personPath(name))
	if err != nil {
		writeError(w, errf(http.StatusNotFound, "not_found", "person %q not found", name))
		return
	}
	pf.Identity = &ident
	data, err := json.MarshalIndent(pf, "", "  ")
	if err == nil {
		err = fsutil.WriteFileAtomic(m.personPath(name), data, 0o644)
	}
	if err != nil {
		writeError(w, errf(http.StatusInternalServerError, "internal", "save identity: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"person": pf.public()})
}
