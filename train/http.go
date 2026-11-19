package train

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

const (
	minPhotos     = 10
	maxPhotos     = 50
	maxPhotoBytes = 30 << 20
	maxTotalBytes = 300 << 20
	maxFieldBytes = 4 << 10
	maxBodyBytes  = maxTotalBytes + 4<<20
)

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9_]{2,32}$`)
	genders  = map[string]bool{"man": true, "woman": true, "person": true}
	extByFmt = map[string]string{"jpeg": ".jpg", "png": ".png", "webp": ".webp"}
)

type httpError struct {
	status int
	code   string
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func errf(status int, code, format string, a ...any) *httpError {
	return &httpError{status: status, code: code, msg: fmt.Sprintf(format, a...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[train] write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, e *httpError) {
	writeJSON(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": e.msg}})
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		writeError(w, errf(http.StatusForbidden, "forbidden", "browser requests are not allowed"))
		return
	}
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/train"), "/")
	switch {
	case p == "jobs" && r.Method == http.MethodPost:
		m.handleSubmit(w, r)
	case p == "jobs" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"jobs": m.list(r.URL.Query().Get("client"))})
	case strings.HasPrefix(p, "jobs/"):
		m.handleJob(w, r, strings.TrimPrefix(p, "jobs/"))
	case p == "bases" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"bases": m.Bases()})
	case p == "people" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"people": m.people()})
	case strings.HasPrefix(p, "people/") && strings.HasSuffix(p, "/rename") && r.Method == http.MethodPost:
		m.handleRenamePerson(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "people/"), "/rename"))
	case strings.HasPrefix(p, "people/") && strings.HasSuffix(p, "/identity") && r.Method == http.MethodPost:
		m.handleIdentity(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "people/"), "/identity"))
	case strings.HasPrefix(p, "people/") && r.Method == http.MethodDelete:
		m.handleDeletePerson(w, strings.TrimPrefix(p, "people/"))
	default:
		writeError(w, errf(http.StatusNotFound, "not_found", "not found: %s %s", r.Method, r.URL.Path))
	}
}

func (m *Manager) handleJob(w http.ResponseWriter, r *http.Request, rest string) {
	id, action, _ := strings.Cut(rest, "/")
	var (
		j  Job
		ok bool
	)
	switch {
	case action == "" && r.Method == http.MethodGet:
		j, ok = m.get(id)
	case action == "cancel" && r.Method == http.MethodPost:
		j, ok = m.cancelJob(id)
	case (action == "pause" || action == "resume") && r.Method == http.MethodPost:
		var code string
		if action == "pause" {
			j, code, ok = m.pauseJob(id)
		} else {
			j, code, ok = m.resumeJob(id)
		}
		if ok && code != "" {
			writeError(w, errf(http.StatusConflict, code, "training job %s is %s", id, j.Status))
			return
		}
	default:
		writeError(w, errf(http.StatusNotFound, "not_found", "not found: %s %s", r.Method, r.URL.Path))
		return
	}
	if !ok {
		writeError(w, errf(http.StatusNotFound, "not_found", "training job %s not found", id))
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (m *Manager) handleDeletePerson(w http.ResponseWriter, name string) {
	if !nameRe.MatchString(name) {
		writeError(w, errf(http.StatusNotFound, "not_found", "person %q not found", name))
		return
	}
	status, code := m.deletePerson(name)
	switch status {
	case http.StatusOK:
		writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
	case http.StatusConflict:
		writeError(w, errf(status, code, "person %q is being trained", name))
	case http.StatusNotFound:
		writeError(w, errf(status, code, "person %q not found", name))
	default:
		writeError(w, errf(status, code, "cannot delete person %q", name))
	}
}

func (m *Manager) handleRenamePerson(w http.ResponseWriter, r *http.Request, name string) {
	if !nameRe.MatchString(name) {
		writeError(w, errf(http.StatusNotFound, "not_found", "person %q not found", name))
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxFieldBytes)).Decode(&body); err != nil {
		writeError(w, errf(http.StatusBadRequest, "invalid_request", "expected JSON body {\"name\": ...}: %v", err))
		return
	}
	to := normalizeName(body.Name)
	if !nameRe.MatchString(to) {
		writeError(w, errf(http.StatusBadRequest, "invalid_request", "name must be 2-32 characters: latin letters, digits, _"))
		return
	}
	p, status, code := m.renamePerson(name, to)
	switch status {
	case http.StatusOK:
		writeJSON(w, http.StatusOK, map[string]any{"person": p})
	case http.StatusNotFound:
		writeError(w, errf(status, code, "person %q not found", name))
	case http.StatusConflict:
		writeError(w, errf(status, code, "name %q is already used", to))
	default:
		writeError(w, errf(status, code, "cannot rename person %q", name))
	}
}

type submitForm struct {
	name, gender, base, client, clientRef string
	photos                                int
}

func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return '_'
		}
		return r
	}, s)
}

func (m *Manager) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if !m.Available() {
		writeError(w, errf(http.StatusServiceUnavailable, "not_available", "LoRA training is not installed on this server"))
		return
	}
	id, err := newID()
	if err != nil {
		writeError(w, errf(http.StatusInternalServerError, "internal", "%v", err))
		return
	}
	dir := m.jobDir(id)
	keep := false
	defer func() {
		if !keep {
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("[train] remove rejected upload %s: %v", id, err)
			}
		}
	}()
	form, herr := m.readUpload(w, r, filepath.Join(dir, datasetDir))
	if herr != nil {
		writeError(w, herr)
		return
	}
	if herr := validateForm(form); herr != nil {
		writeError(w, herr)
		return
	}
	if form.base == "" {
		form.base = baseZImage
	}
	if !m.baseAvailable(form.base) {
		writeError(w, errf(http.StatusBadRequest, "base_unavailable", "%s model files are missing on the server", form.base))
		return
	}
	trigger := form.name + "_" + form.gender
	if err := writeCaptions(filepath.Join(dir, datasetDir), form.gender); err != nil {
		writeError(w, errf(http.StatusInternalServerError, "internal", "write captions: %v", err))
		return
	}

	m.mu.Lock()
	if form.clientRef != "" {
		for _, j := range m.jobs {
			if j.active() && j.Client == form.client && j.ClientRef == form.clientRef {
				snap := m.snapshot(j)
				m.mu.Unlock()
				writeJSON(w, http.StatusOK, snap)
				return
			}
		}
	}
	if m.nameTakenLocked(form.name) {
		m.mu.Unlock()
		writeError(w, errf(http.StatusConflict, "name_taken", "name %q is already used", form.name))
		return
	}
	now := time.Now()
	j := &Job{
		ID: id, Client: form.client, ClientRef: form.clientRef, Name: form.name, Trigger: trigger,
		Gender: form.gender, Base: form.base, Photos: form.photos, Status: statusQueued,
		Progress: Progress{Steps: m.cfg.Steps}, CreatedAt: &now,
	}
	m.jobs[id] = j
	m.persist(j)
	snap := m.snapshot(j)
	keep = true
	m.mu.Unlock()
	m.notify()
	writeJSON(w, http.StatusAccepted, snap)
}

func validateForm(f submitForm) *httpError {
	if !nameRe.MatchString(f.name) {
		return errf(http.StatusBadRequest, "invalid_request", "name must be 2-32 characters: latin letters, digits, _")
	}
	if !genders[f.gender] {
		return errf(http.StatusBadRequest, "invalid_request", "gender must be man, woman or person")
	}
	if f.base != "" && !knownBase(f.base) {
		return errf(http.StatusBadRequest, "invalid_request", "base must be %s, %s, %s, %s or %s", baseZImage, baseMoss, baseSDXL, baseFlux2, baseFlux1)
	}
	if f.photos < minPhotos || f.photos > maxPhotos {
		return errf(http.StatusBadRequest, "invalid_request", "need %d-%d photos, got %d", minPhotos, maxPhotos, f.photos)
	}
	return nil
}

func (m *Manager) readUpload(w http.ResponseWriter, r *http.Request, dst string) (submitForm, *httpError) {
	var f submitForm
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		return f, errf(http.StatusBadRequest, "invalid_request", "expected multipart/form-data: %v", err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return f, errf(http.StatusInternalServerError, "internal", "%v", err)
	}
	var total int64
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return f, uploadErr(err)
		}
		herr := m.readPart(part, dst, &f, &total)
		if cerr := part.Close(); cerr != nil && herr == nil {
			herr = uploadErr(cerr)
		}
		if herr != nil {
			return f, herr
		}
	}
	return f, nil
}

func (m *Manager) readPart(part *multipart.Part, dst string, f *submitForm, total *int64) *httpError {
	if part.FileName() == "" {
		b, err := io.ReadAll(io.LimitReader(part, maxFieldBytes))
		if err != nil {
			return uploadErr(err)
		}
		v := strings.TrimSpace(string(b))
		switch part.FormName() {
		case "name":
			f.name = normalizeName(v)
		case "gender":
			f.gender = strings.ToLower(v)
		case "base":
			f.base = v
		case "client":
			f.client = v
		case "client_ref":
			f.clientRef = v
		}
		return nil
	}
	if part.FormName() != "photos" {
		return nil
	}
	f.photos++
	if f.photos > maxPhotos {
		return errf(http.StatusBadRequest, "invalid_request", "at most %d photos", maxPhotos)
	}
	return savePhoto(part, dst, f.photos, total)
}

func savePhoto(part io.Reader, dst string, n int, total *int64) *httpError {
	tmp := filepath.Join(dst, fmt.Sprintf("%03d.part", n))
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return errf(http.StatusInternalServerError, "internal", "%v", err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(part, maxPhotoBytes+1))
	if err := out.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		return uploadErr(copyErr)
	}
	*total += written
	if written > maxPhotoBytes {
		return errf(http.StatusRequestEntityTooLarge, "too_large", "photo %d is larger than %d MB", n, maxPhotoBytes>>20)
	}
	if *total > maxTotalBytes {
		return errf(http.StatusRequestEntityTooLarge, "too_large", "photos are larger than %d MB in total", maxTotalBytes>>20)
	}
	format, herr := imageFormat(tmp)
	if herr != nil {
		return herr
	}
	if err := os.Rename(tmp, filepath.Join(dst, fmt.Sprintf("%03d%s", n, extByFmt[format]))); err != nil {
		return errf(http.StatusInternalServerError, "internal", "%v", err)
	}
	return nil
}

func imageFormat(path string) (string, *httpError) {
	in, err := os.Open(path)
	if err != nil {
		return "", errf(http.StatusInternalServerError, "internal", "%v", err)
	}
	defer in.Close()
	cfg, format, err := image.DecodeConfig(in)
	if err != nil || extByFmt[format] == "" {
		return "", errf(http.StatusBadRequest, "invalid_request", "file is not a JPEG, PNG or WebP image")
	}
	if cfg.Width == 0 || cfg.Height == 0 {
		return "", errf(http.StatusBadRequest, "invalid_request", "image is empty")
	}
	return format, nil
}

func uploadErr(err error) *httpError {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return errf(http.StatusRequestEntityTooLarge, "too_large", "request is larger than %d MB", maxBodyBytes>>20)
	}
	return errf(http.StatusBadRequest, "invalid_request", "bad upload: %v", err)
}
