package sdcpp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"imagecaster-server/fsutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"
)

type jobStore struct {
	dir string
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	jobs    map[string]*job
	refs    map[string]string
	running string

	pmu sync.Mutex
}

var jobIDPattern = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

func newJobStore(dir string, ttl time.Duration, now func() time.Time) *jobStore {
	s := &jobStore{dir: dir, ttl: ttl, now: now, jobs: map[string]*job{}, refs: map[string]string{}}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("[sdcpp] jobs dir %s: %v", dir, err)
		} else if err := os.Chmod(dir, 0o700); err != nil {
			log.Printf("[sdcpp] jobs dir %s: %v", dir, err)
		}
		s.restore()
		go s.janitor()
	}
	return s
}

func refKey(client, ref string) string { return client + "\x00" + ref }

func tptr(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func terminal(status string) bool {
	switch status {
	case statusCompleted, statusFailed, statusCancelled, statusLost:
		return true
	}
	return false
}

func newJobID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(b)
}

func (s *jobStore) jobDir(id string) string { return filepath.Join(s.dir, id) }

func (s *jobStore) restore() {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		log.Printf("[sdcpp] read jobs dir %s: %v", s.dir, err)
		return
	}
	now := s.now()
	for _, e := range ents {
		if !e.IsDir() || !jobIDPattern.MatchString(e.Name()) {
			continue
		}
		dir := s.jobDir(e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "job.json"))
		var st JobStatus
		if err != nil || json.Unmarshal(b, &st) != nil || st.ID != e.Name() {
			os.RemoveAll(dir)
			continue
		}
		if st.Status == statusCompleted {
			if _, err := os.Stat(filepath.Join(dir, "result.json")); err != nil {
				os.RemoveAll(dir)
				continue
			}
		}
		if !terminal(st.Status) {
			st.Status = statusLost
			st.Position = 0
			st.Error = &JobError{Code: "server_restarted", Message: "the server restarted before the job finished"}
			st.FinishedAt = tptr(now)
			st.ExpiresAt = tptr(now.Add(s.ttl))
		}
		if st.ExpiresAt != nil && !now.Before(*st.ExpiresAt) {
			os.RemoveAll(dir)
			continue
		}
		removeStaleInputs(dir)
		tightenPerms(dir)
		j := &job{st: st, kind: st.Kind}
		s.jobs[st.ID] = j
		if st.ClientRef != "" {
			if old, ok := s.jobs[s.refs[refKey(st.Client, st.ClientRef)]]; !ok || old.st.CreatedAt == nil || st.CreatedAt == nil || old.st.CreatedAt.Before(*st.CreatedAt) {
				s.refs[refKey(st.Client, st.ClientRef)] = st.ID
			}
		}
		s.writeStatus(st)
	}
}

func tightenPerms(dir string) {
	if err := os.Chmod(dir, 0o700); err != nil {
		log.Printf("[sdcpp] chmod %s: %v", dir, err)
	}
	for _, name := range []string{"job.json", "result.json"} {
		if err := os.Chmod(filepath.Join(dir, name), 0o600); err != nil && !os.IsNotExist(err) {
			log.Printf("[sdcpp] chmod %s: %v", name, err)
		}
	}
}

func (s *jobStore) writeStatus(st JobStatus) {
	if s.dir == "" {
		return
	}
	dir := s.jobDir(st.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[sdcpp] job %s: %v", st.ID, err)
		return
	}
	st.Position = 0
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	writeAtomic(filepath.Join(dir, "job.json"), b)
}

func writeAtomic(path string, b []byte) error {
	if err := fsutil.WriteFileAtomic(path, b, 0o600); err != nil {
		log.Printf("[sdcpp] write %s: %v", path, err)
		return err
	}
	return nil
}

func (s *jobStore) persist(j *job) {
	if s.dir == "" {
		return
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	s.mu.Lock()
	st, deleted := j.st, j.deleted
	s.mu.Unlock()
	if deleted {
		return
	}
	s.writeStatus(st)
}

func (s *jobStore) removeFiles(id string) {
	if s.dir == "" {
		return
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if err := os.RemoveAll(s.jobDir(id)); err != nil {
		log.Printf("[sdcpp] remove job %s: %v", id, err)
	}
}

func (s *jobStore) sweep() {
	now := s.now()
	var expired []string
	s.mu.Lock()
	for id, j := range s.jobs {
		if terminal(j.st.Status) && j.st.ExpiresAt != nil && !now.Before(*j.st.ExpiresAt) {
			s.dropLocked(id, j)
			expired = append(expired, id)
		}
	}
	s.mu.Unlock()
	for _, id := range expired {
		s.removeFiles(id)
	}
}

func (s *jobStore) dropLocked(id string, j *job) {
	j.deleted = true
	delete(s.jobs, id)
	if j.st.ClientRef != "" && s.refs[refKey(j.st.Client, j.st.ClientRef)] == id {
		delete(s.refs, refKey(j.st.Client, j.st.ClientRef))
	}
}

func (s *jobStore) markRunning(j *job, step, steps int) {
	s.mu.Lock()
	if steps > 0 {
		j.st.Progress = JobProgress{Step: step, Steps: steps}
	}
	if j.st.Status != statusQueued {
		s.mu.Unlock()
		return
	}
	j.st.Status = statusRunning
	j.st.StartedAt = tptr(s.now())
	s.running = j.st.ID
	s.mu.Unlock()
	s.persist(j)
}

func (s *jobStore) runningID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *jobStore) lastQueuedModelLocked() (ModelSpec, bool) {
	var last *job
	for _, j := range s.jobs {
		if terminal(j.st.Status) || j.kind == kindUpscale || cleanName(j.model.Checkpoint) == "" {
			continue
		}
		if last == nil || j.st.CreatedAt.After(*last.st.CreatedAt) || (j.st.CreatedAt.Equal(*last.st.CreatedAt) && j.st.ID > last.st.ID) {
			last = j
		}
	}
	if last == nil {
		return ModelSpec{}, false
	}
	return last.model, true
}

func (s *jobStore) janitor() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.sweep()
	}
}

const inputPrefix = "input-"

func inputFields(p *GenParams) map[string]*string {
	return map[string]*string{"init_image": &p.InitImage, "mask": &p.Mask, "image": &p.Image}
}

func (s *jobStore) stashInputs(id string, p *GenParams) []string {
	if s.dir == "" {
		return nil
	}
	dir := s.jobDir(id)
	var stashed []string
	for name, field := range inputFields(p) {
		if *field == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("[sdcpp] job %s: keep %s in memory: %v", id, name, err)
			continue
		}
		if err := writeAtomic(filepath.Join(dir, inputPrefix+name), []byte(*field)); err != nil {
			continue
		}
		*field = ""
		stashed = append(stashed, name)
	}
	return stashed
}

func (s *jobStore) discardStash(id string, stashed []string) {
	if len(stashed) > 0 {
		s.removeFiles(id)
	}
}

func (s *jobStore) loadInputs(j *job, p *GenParams) error {
	s.mu.Lock()
	names := slices.Clone(j.stashed)
	s.mu.Unlock()
	fields := inputFields(p)
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(s.jobDir(j.st.ID), inputPrefix+name))
		if err != nil {
			return apiErr(http.StatusInternalServerError, "sd_failed", "read job input %s: %v", name, err)
		}
		*fields[name] = string(b)
	}
	return nil
}

func (s *jobStore) dropInputs(j *job) {
	s.mu.Lock()
	names := j.stashed
	j.stashed = nil
	s.mu.Unlock()
	if len(names) == 0 {
		return
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	for _, name := range names {
		if err := os.Remove(filepath.Join(s.jobDir(j.st.ID), inputPrefix+name)); err != nil && !os.IsNotExist(err) {
			log.Printf("[sdcpp] job %s: remove input %s: %v", j.st.ID, name, err)
		}
	}
}

func removeStaleInputs(dir string) {
	for _, name := range []string{"init_image", "mask", "image"} {
		if err := os.Remove(filepath.Join(dir, inputPrefix+name)); err != nil && !os.IsNotExist(err) {
			log.Printf("[sdcpp] remove input %s: %v", name, err)
		}
	}
}
