package train

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"

	"imagecaster-server/fsutil"
)

const (
	modeFull     = "full"
	modeQuick    = "quick"
	modeContinue = "continue"
	modeRetrain  = "retrain"

	quickSteps    = 600
	continueSteps = 400
	newRepeats    = 3

	newDatasetDir = "dataset_new"
)

var modes = map[string]bool{"": true, modeFull: true, modeQuick: true, modeContinue: true, modeRetrain: true}

func replacesPerson(mode string) bool {
	return mode == modeContinue || mode == modeRetrain
}

func (m *Manager) modeSteps(mode string) int {
	switch mode {
	case modeQuick:
		return quickSteps
	case modeContinue:
		return continueSteps
	}
	return m.cfg.Steps
}

func (m *Manager) jobSteps(j *Job) int {
	if j.Steps > 0 {
		return j.Steps
	}
	return m.cfg.Steps
}

func (m *Manager) prevDir() string {
	if m.cfg.PrevDir != "" {
		return m.cfg.PrevDir
	}
	return filepath.Join(filepath.Dir(m.cfg.JobsDir), "trainprev")
}

func (m *Manager) prevLora(name string) string {
	return filepath.Join(m.prevDir(), name+".safetensors")
}

func (m *Manager) prevCard(name string) string {
	return filepath.Join(m.prevDir(), name+".person.json")
}

func (m *Manager) hasPrev(name string) bool {
	return fsutil.FileExists(m.prevLora(name)) && fsutil.FileExists(m.prevCard(name))
}

func copyAtomic(src, dst string) error {
	if err := copyFile(src, dst+".tmp"); err != nil {
		os.Remove(dst + ".tmp")
		return err
	}
	if err := fsutil.Rename(dst+".tmp", dst); err != nil {
		os.Remove(dst + ".tmp")
		return err
	}
	return nil
}

func (m *Manager) stagePrev(name string) error {
	if err := os.MkdirAll(m.prevDir(), 0o755); err != nil {
		return err
	}
	if err := copyAtomic(filepath.Join(m.cfg.LoraDir, name+".safetensors"), m.prevLora(name)+".pending"); err != nil {
		return err
	}
	if err := copyAtomic(m.personPath(name), m.prevCard(name)+".pending"); err != nil {
		os.Remove(m.prevLora(name) + ".pending")
		return err
	}
	return nil
}

func (m *Manager) commitPrev(name string) error {
	if err := fsutil.Rename(m.prevLora(name)+".pending", m.prevLora(name)); err != nil {
		return err
	}
	return fsutil.Rename(m.prevCard(name)+".pending", m.prevCard(name))
}

func (m *Manager) dropPending(name string) {
	os.Remove(m.prevLora(name) + ".pending")
	os.Remove(m.prevCard(name) + ".pending")
}

func (m *Manager) restoreFromPending(name string) error {
	if err := copyAtomic(m.prevLora(name)+".pending", filepath.Join(m.cfg.LoraDir, name+".safetensors")); err != nil {
		return err
	}
	return copyAtomic(m.prevCard(name)+".pending", m.personPath(name))
}

func (m *Manager) restoreFromPrev(name string) error {
	if err := copyAtomic(m.prevLora(name), filepath.Join(m.cfg.LoraDir, name+".safetensors")); err != nil {
		return err
	}
	return copyAtomic(m.prevCard(name), m.personPath(name))
}

func (m *Manager) rollbackPerson(name string) (Person, int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.Name == name && j.holdsName() {
			return Person{}, 409, "busy"
		}
	}
	cur := filepath.Join(m.cfg.LoraDir, name+".safetensors")
	if !fsutil.FileExists(m.personPath(name)) || !fsutil.FileExists(cur) {
		return Person{}, 404, "not_found"
	}
	if !m.hasPrev(name) {
		return Person{}, 409, "no_previous"
	}
	swap := filepath.Join(m.prevDir(), name+".swap")
	if err := copyFile(cur, swap+".safetensors"); err != nil {
		log.Printf("[train] rollback %s: %v", name, err)
		return Person{}, 500, "rollback_failed"
	}
	defer os.Remove(swap + ".safetensors")
	if err := copyFile(m.personPath(name), swap+".person.json"); err != nil {
		log.Printf("[train] rollback %s: %v", name, err)
		return Person{}, 500, "rollback_failed"
	}
	defer os.Remove(swap + ".person.json")
	if err := m.restoreFromPrev(name); err != nil {
		log.Printf("[train] rollback %s: %v", name, err)
		if rbErr := copyAtomic(swap+".safetensors", cur); rbErr != nil {
			log.Printf("[train] rollback %s: restore lora: %v", name, rbErr)
		}
		if rbErr := copyAtomic(swap+".person.json", m.personPath(name)); rbErr != nil {
			log.Printf("[train] rollback %s: restore card: %v", name, rbErr)
		}
		return Person{}, 500, "rollback_failed"
	}
	if err := copyAtomic(swap+".safetensors", m.prevLora(name)); err != nil {
		log.Printf("[train] rollback %s: keep replaced version: %v", name, err)
		os.Remove(m.prevLora(name))
		os.Remove(m.prevCard(name))
	} else if err := copyAtomic(swap+".person.json", m.prevCard(name)); err != nil {
		log.Printf("[train] rollback %s: keep replaced card: %v", name, err)
		os.Remove(m.prevLora(name))
		os.Remove(m.prevCard(name))
	}
	pf, err := readPersonFile(m.personPath(name))
	if err != nil {
		return Person{}, 500, "rollback_failed"
	}
	p := pf.public()
	p.Previous = m.hasPrev(name)
	return p, 200, ""
}

func (m *Manager) renamePrev(from, to string) {
	if !m.hasPrev(from) {
		return
	}
	pf, err := readPersonFile(m.prevCard(from))
	if err != nil {
		log.Printf("[train] rename previous %s: %v", from, err)
		return
	}
	pf.Name, pf.Lora = to, to
	data, err := json.MarshalIndent(pf, "", "  ")
	if err == nil {
		err = fsutil.WriteFileAtomic(m.prevCard(to), data, 0o644)
	}
	if err == nil {
		err = fsutil.Rename(m.prevLora(from), m.prevLora(to))
	}
	if err != nil {
		log.Printf("[train] rename previous %s: %v", from, err)
		os.Remove(m.prevCard(to))
		return
	}
	if err := os.Remove(m.prevCard(from)); err != nil {
		log.Printf("[train] rename previous %s: %v", from, err)
	}
}

func (m *Manager) removePrev(name string) error {
	for _, p := range []string{m.prevLora(name), m.prevCard(name)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
