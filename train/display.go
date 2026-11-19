package train

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"imagecaster-server/fsutil"
)

const maxDisplayRunes = 40

var errDisplayInvalid = errors.New("display name must be up to 40 characters without control characters")

func normalizeDisplay(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errDisplayInvalid
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxDisplayRunes {
		return "", errDisplayInvalid
	}
	return s, nil
}

func personBase(base string) string {
	if base == "" {
		return baseZImage
	}
	return base
}

func (m *Manager) displayHolderLocked(display, base, except string) string {
	if display == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(m.cfg.LoraDir, "*.person.json"))
	if err != nil {
		return ""
	}
	for _, path := range matches {
		pf, err := readPersonFile(path)
		if err != nil || pf.Name == "" || pf.Name == except {
			continue
		}
		if personBase(pf.Base) == personBase(base) && strings.EqualFold(pf.DisplayName, display) {
			return pf.Name
		}
	}
	return ""
}

func (m *Manager) canonicalDisplayLocked(display string) string {
	if display == "" {
		return ""
	}
	for _, j := range m.jobs {
		if j.holdsName() && j.DisplayName != "" && strings.EqualFold(j.DisplayName, display) {
			return j.DisplayName
		}
	}
	matches, _ := filepath.Glob(filepath.Join(m.cfg.LoraDir, "*.person.json"))
	for _, path := range matches {
		if pf, err := readPersonFile(path); err == nil && pf.DisplayName != "" && strings.EqualFold(pf.DisplayName, display) {
			return pf.DisplayName
		}
	}
	return display
}

func (m *Manager) displayFoldLocked(s string) (string, bool) {
	c := m.canonicalDisplayLocked(s)
	if c != s {
		return c, true
	}
	for _, j := range m.jobs {
		if j.holdsName() && strings.EqualFold(j.DisplayName, s) {
			return c, true
		}
	}
	matches, _ := filepath.Glob(filepath.Join(m.cfg.LoraDir, "*.person.json"))
	for _, path := range matches {
		if pf, err := readPersonFile(path); err == nil && strings.EqualFold(pf.DisplayName, s) {
			return c, true
		}
	}
	return "", false
}

func (m *Manager) displayJobHolderLocked(display, base, except string) string {
	if display == "" {
		return ""
	}
	for _, j := range m.jobs {
		if j.Name != except && j.holdsName() && personBase(j.Base) == personBase(base) && strings.EqualFold(j.DisplayName, display) {
			return j.Name
		}
	}
	return ""
}

func (m *Manager) techNameClashLocked(display, except string) string {
	if display == "" {
		return ""
	}
	for _, j := range m.jobs {
		if j.holdsName() && j.Name != except && strings.EqualFold(j.Name, display) && !strings.EqualFold(j.DisplayName, display) {
			return j.Name
		}
	}
	matches, _ := filepath.Glob(filepath.Join(m.cfg.LoraDir, "*.person.json"))
	for _, path := range matches {
		pf, err := readPersonFile(path)
		if err == nil && pf.Name != except && strings.EqualFold(pf.Name, display) && !strings.EqualFold(pf.DisplayName, display) {
			return pf.Name
		}
	}
	return ""
}

func (m *Manager) clearJobDisplayLocked(name string) {
	for _, j := range m.jobs {
		if j.Name == name && j.holdsName() {
			j.DisplayName, j.DisplayReplace = "", false
			m.persist(j)
		}
	}
}

func (m *Manager) clearDisplayLocked(holder string) error {
	hp, err := readPersonFile(m.personPath(holder))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	hp.DisplayName = ""
	return m.writePersonLocked(holder, hp)
}

func (m *Manager) writePersonLocked(name string, pf personFile) error {
	out, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(m.personPath(name), out, 0o644)
}

func (m *Manager) setDisplay(name, display string, replace bool) (Person, int, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pf, err := readPersonFile(m.personPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return Person{}, http.StatusNotFound, "not_found", ""
	}
	if err != nil {
		return Person{}, http.StatusInternalServerError, "internal", ""
	}
	display = m.canonicalDisplayLocked(display)
	if clash := m.techNameClashLocked(display, name); clash != "" {
		return Person{}, http.StatusConflict, "display_taken", clash
	}
	if job := m.displayJobHolderLocked(display, pf.Base, name); job != "" {
		if !replace {
			return Person{}, http.StatusConflict, "display_taken", job
		}
		m.clearJobDisplayLocked(job)
	}
	if holder := m.displayHolderLocked(display, pf.Base, name); holder != "" {
		if !replace {
			return Person{}, http.StatusConflict, "display_taken", holder
		}
		if err := m.clearDisplayLocked(holder); err != nil {
			log.Printf("[train] clear display of %s: %v", holder, err)
			return Person{}, http.StatusInternalServerError, "internal", ""
		}
	}
	pf.DisplayName = display
	if err := m.writePersonLocked(name, pf); err != nil {
		log.Printf("[train] write %s: %v", m.personPath(name), err)
		return Person{}, http.StatusInternalServerError, "internal", ""
	}
	return pf.public(), http.StatusOK, "", ""
}

func (m *Manager) handleDisplay(w http.ResponseWriter, r *http.Request, name string) {
	if !nameRe.MatchString(name) {
		writeError(w, errf(http.StatusNotFound, "not_found", "person %q not found", name))
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
		Replace     bool   `json:"replace"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxFieldBytes)).Decode(&body); err != nil {
		writeError(w, errf(http.StatusBadRequest, "invalid_request", "expected JSON body {\"display_name\": ...}: %v", err))
		return
	}
	display, err := normalizeDisplay(body.DisplayName)
	if err != nil {
		writeError(w, errf(http.StatusBadRequest, "invalid_request", "%v", err))
		return
	}
	p, status, code, holder := m.setDisplay(name, display, body.Replace)
	switch status {
	case http.StatusOK:
		writeJSON(w, http.StatusOK, map[string]any{"person": p})
	case http.StatusNotFound:
		writeError(w, errf(status, code, "person %q not found", name))
	case http.StatusConflict:
		writeError(w, errf(status, code, "display name %q is already used by %s", display, holder))
	default:
		writeError(w, errf(status, code, "cannot set display name of %q", name))
	}
}
