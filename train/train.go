package train

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"imagecaster-server/fsutil"
)

const (
	defaultSteps = 2000
	baseModel    = "Z-Image"
	statusFile   = "status.json"
	datasetDir   = "dataset"
	jobTTL       = 7 * 24 * time.Hour
)

const (
	statusQueued    = "queued"
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
	statusLost      = "lost"
)

type Config struct {
	Python     string
	ToolkitDir string
	JobsDir    string
	LoraDir    string
	Steps      int
}

type RunSpec struct {
	WorkDir    string
	ConfigPath string
	DatasetDir string
	OutputPath string
	LogPath    string
	Steps      int
}

type Runner interface {
	Run(ctx context.Context, spec RunSpec, progress func(step, steps int)) error
}

type Leaser func(ctx context.Context, client string) (release func(), err error)

type Progress struct {
	Step  int `json:"step"`
	Steps int `json:"steps"`
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Job struct {
	ID         string     `json:"id"`
	Client     string     `json:"client"`
	ClientRef  string     `json:"client_ref"`
	Name       string     `json:"name"`
	Trigger    string     `json:"trigger"`
	Gender     string     `json:"gender"`
	Photos     int        `json:"photos"`
	Status     string     `json:"status"`
	Progress   Progress   `json:"progress"`
	Error      *JobError  `json:"error"`
	CreatedAt  *time.Time `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (j *Job) active() bool {
	return j.Status == statusQueued || j.Status == statusRunning
}

type Person struct {
	Name      string     `json:"name"`
	Trigger   string     `json:"trigger"`
	Gender    string     `json:"gender"`
	Base      string     `json:"base"`
	Lora      string     `json:"lora"`
	Steps     int        `json:"steps"`
	CreatedAt *time.Time `json:"created_at"`
}

type Manager struct {
	cfg   Config
	run   Runner
	lease Leaser

	mu        sync.Mutex
	idle      *sync.Cond
	jobs      map[string]*Job
	busy      bool
	cancelCur context.CancelCauseFunc
	curID     string

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

var (
	errCancelled = errors.New("cancelled")
	errLeaseLost = errors.New("gpu lease lost")
	errStopped   = errors.New("server stopped")
)

func New(cfg Config, run Runner, lease Leaser) (*Manager, error) {
	if cfg.Steps <= 0 {
		cfg.Steps = defaultSteps
	}
	if cfg.JobsDir == "" {
		return nil, errors.New("train: jobs dir is not set")
	}
	if err := os.MkdirAll(cfg.JobsDir, 0o700); err != nil {
		return nil, fmt.Errorf("train: %w", err)
	}
	if err := os.Chmod(cfg.JobsDir, 0o700); err != nil {
		return nil, fmt.Errorf("train: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cfg:    cfg,
		run:    run,
		lease:  lease,
		jobs:   map[string]*Job{},
		wake:   make(chan struct{}, 1),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.idle = sync.NewCond(&m.mu)
	m.restore()
	go m.worker()
	return m, nil
}

func (m *Manager) Available() bool {
	if m.cfg.Python == "" || m.cfg.ToolkitDir == "" || m.cfg.LoraDir == "" {
		return false
	}
	if st, err := os.Stat(m.cfg.Python); err != nil || st.IsDir() {
		return false
	}
	st, err := os.Stat(filepath.Join(m.cfg.ToolkitDir, "run.py"))
	return err == nil && !st.IsDir()
}

func (m *Manager) Close() {
	m.cancel()
	<-m.done
}

func (m *Manager) Wait() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for m.busy || m.hasQueuedLocked() {
		m.idle.Wait()
	}
}

func (m *Manager) CancelRunning() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancelCur != nil {
		m.cancelCur(errLeaseLost)
	}
}

func (m *Manager) hasQueuedLocked() bool {
	for _, j := range m.jobs {
		if j.Status == statusQueued {
			return true
		}
	}
	return false
}

func (m *Manager) jobDir(id string) string {
	return filepath.Join(m.cfg.JobsDir, id)
}

func (m *Manager) restore() {
	entries, err := os.ReadDir(m.cfg.JobsDir)
	if err != nil {
		log.Printf("[train] read jobs dir: %v", err)
		return
	}
	now := time.Now()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(m.cfg.JobsDir, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, statusFile))
		if err != nil {
			if rmErr := os.RemoveAll(dir); rmErr != nil {
				log.Printf("[train] remove broken job dir %s: %v", dir, rmErr)
			}
			continue
		}
		var j Job
		if err := json.Unmarshal(data, &j); err != nil || j.ID != e.Name() {
			if rmErr := os.RemoveAll(dir); rmErr != nil {
				log.Printf("[train] remove broken job dir %s: %v", dir, rmErr)
			}
			continue
		}
		if j.active() {
			j.Status = statusLost
			j.Error = &JobError{Code: "server_restarted", Message: "the server restarted before training finished"}
			j.FinishedAt = &now
		}
		if j.FinishedAt != nil && now.Sub(*j.FinishedAt) > jobTTL {
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("[train] remove old job %s: %v", j.ID, err)
			}
			continue
		}
		m.removeJobFiles(j.ID)
		m.jobs[j.ID] = &j
		m.persist(&j)
	}
}

func (m *Manager) persist(j *Job) {
	data, err := json.Marshal(j)
	if err != nil {
		log.Printf("[train] encode job %s: %v", j.ID, err)
		return
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(m.jobDir(j.ID), statusFile), data, 0o644); err != nil {
		log.Printf("[train] save job %s: %v", j.ID, err)
	}
}

func (m *Manager) removeJobFiles(id string) {
	dir := m.jobDir(id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == statusFile || e.Name() == "train.log" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			log.Printf("[train] remove %s/%s: %v", id, e.Name(), err)
		}
	}
}

func (m *Manager) snapshot(j *Job) Job {
	c := *j
	if j.Error != nil {
		e := *j.Error
		c.Error = &e
	}
	return c
}

func (m *Manager) get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return m.snapshot(j), true
}

func (m *Manager) list(client string) []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Job{}
	for _, j := range m.jobs {
		if client != "" && j.Client != client {
			continue
		}
		out = append(out, m.snapshot(j))
	}
	sort.Slice(out, func(a, b int) bool {
		ta, tb := out[a].CreatedAt, out[b].CreatedAt
		if ta == nil || tb == nil || ta.Equal(*tb) {
			return out[a].ID < out[b].ID
		}
		return ta.Before(*tb)
	})
	return out
}

func (m *Manager) cancelJob(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	switch {
	case m.curID == id && m.cancelCur != nil:
		m.cancelCur(errCancelled)
	case j.Status == statusQueued:
		now := time.Now()
		j.Status = statusCancelled
		j.FinishedAt = &now
		m.removeJobFiles(id)
		m.persist(j)
		m.idle.Broadcast()
	}
	return m.snapshot(j), true
}

func (m *Manager) worker() {
	defer close(m.done)
	for {
		j := m.nextQueued()
		if j == nil {
			select {
			case <-m.ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}
		m.execute(j)
		if m.ctx.Err() != nil {
			return
		}
	}
}

func (m *Manager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) nextQueued() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var next *Job
	for _, j := range m.jobs {
		if j.Status != statusQueued {
			continue
		}
		if next == nil || j.CreatedAt.Before(*next.CreatedAt) || (j.CreatedAt.Equal(*next.CreatedAt) && j.ID < next.ID) {
			next = j
		}
	}
	if next != nil {
		m.busy = true
	}
	return next
}

func (m *Manager) execute(j *Job) {
	ctx, cancel := context.WithCancelCause(m.ctx)
	defer cancel(nil)

	m.mu.Lock()
	if j.Status != statusQueued {
		m.busy = false
		m.idle.Broadcast()
		m.mu.Unlock()
		return
	}
	id := j.ID
	m.curID = id
	m.cancelCur = cancel
	m.mu.Unlock()

	err := m.train(ctx, j)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.curID = ""
	m.cancelCur = nil
	m.busy = false
	end := time.Now()
	j.FinishedAt = &end
	cause := context.Cause(ctx)
	switch {
	case err == nil:
		j.Status = statusCompleted
		j.Progress.Step = j.Progress.Steps
	case errors.Is(cause, errCancelled):
		j.Status = statusCancelled
	case m.ctx.Err() != nil:
		j.Status = statusLost
		j.Error = &JobError{Code: "server_stopped", Message: errStopped.Error()}
	case errors.Is(cause, errLeaseLost):
		j.Status = statusFailed
		j.Error = &JobError{Code: "gpu_lease_lost", Message: errLeaseLost.Error()}
	default:
		j.Status = statusFailed
		j.Error = &JobError{Code: codeOf(err), Message: err.Error()}
	}
	m.removeJobFiles(id)
	m.persist(j)
	m.idle.Broadcast()
}

type codedError struct {
	code string
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func codeOf(err error) string {
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code
	}
	return "train_failed"
}

func (m *Manager) train(ctx context.Context, j *Job) error {
	dir := m.jobDir(j.ID)
	spec := RunSpec{
		WorkDir:    dir,
		ConfigPath: filepath.Join(dir, "config.yaml"),
		DatasetDir: filepath.Join(dir, datasetDir),
		OutputPath: filepath.Join(dir, "output", j.Name, j.Name+".safetensors"),
		LogPath:    filepath.Join(dir, "train.log"),
		Steps:      m.cfg.Steps,
	}
	if err := writeTrainConfig(spec, j.Name, j.Trigger); err != nil {
		return &codedError{code: "config_failed", err: err}
	}
	if m.lease != nil {
		release, err := m.lease(ctx, "lora-train")
		if err != nil {
			return &codedError{code: "gpu_unavailable", err: err}
		}
		defer release()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	now := time.Now()
	j.Status = statusRunning
	j.StartedAt = &now
	j.Progress = Progress{Step: 0, Steps: m.cfg.Steps}
	m.persist(j)
	m.mu.Unlock()
	err := m.run.Run(ctx, spec, func(step, steps int) {
		m.mu.Lock()
		j.Progress = Progress{Step: step, Steps: steps}
		m.mu.Unlock()
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, err := os.Stat(spec.OutputPath); err != nil {
		return &codedError{code: "no_output", err: errors.New("trainer finished without a LoRA file")}
	}
	return m.installPerson(j, spec.OutputPath)
}

func (m *Manager) installPerson(j *Job, output string) error {
	dst := filepath.Join(m.cfg.LoraDir, j.Name+".safetensors")
	if err := os.MkdirAll(m.cfg.LoraDir, 0o755); err != nil {
		return &codedError{code: "install_failed", err: err}
	}
	if err := copyFile(output, dst+".tmp"); err != nil {
		return &codedError{code: "install_failed", err: err}
	}
	if err := fsutil.Rename(dst+".tmp", dst); err != nil {
		if rmErr := os.Remove(dst + ".tmp"); rmErr != nil {
			log.Printf("[train] remove %s.tmp: %v", dst, rmErr)
		}
		return &codedError{code: "install_failed", err: err}
	}
	now := time.Now()
	p := Person{Name: j.Name, Trigger: j.Trigger, Gender: j.Gender, Base: baseModel, Lora: j.Name, Steps: m.cfg.Steps, CreatedAt: &now}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return &codedError{code: "install_failed", err: err}
	}
	if err := fsutil.WriteFileAtomic(m.personPath(j.Name), data, 0o644); err != nil {
		if rmErr := os.Remove(dst); rmErr != nil {
			log.Printf("[train] remove %s: %v", dst, rmErr)
		}
		return &codedError{code: "install_failed", err: err}
	}
	return nil
}

func (m *Manager) personPath(name string) string {
	return filepath.Join(m.cfg.LoraDir, name+".person.json")
}

func (m *Manager) people() []Person {
	out := []Person{}
	matches, err := filepath.Glob(filepath.Join(m.cfg.LoraDir, "*.person.json"))
	if err != nil {
		return out
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p Person
		if err := json.Unmarshal(data, &p); err != nil || p.Name == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(m.cfg.LoraDir, p.Lora+".safetensors")); err != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func (m *Manager) nameTakenLocked(name string) bool {
	for _, j := range m.jobs {
		if j.Name == name && j.active() {
			return true
		}
	}
	if _, err := os.Stat(m.personPath(name)); err == nil {
		return true
	}
	return loraExists(m.cfg.LoraDir, name)
}

var loraExts = map[string]bool{".gguf": true, ".pt": true, ".pth": true, ".safetensors": true, ".ckpt": true, ".sft": true}

func loraExists(dir, name string) bool {
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		ext := filepath.Ext(d.Name())
		if !d.IsDir() && loraExts[strings.ToLower(ext)] && strings.EqualFold(strings.TrimSuffix(d.Name(), ext), name) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		log.Printf("[train] scan lora dir: %v", err)
	}
	return found
}

func (m *Manager) deletePerson(name string) (int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.Name == name && j.active() {
			return 409, "busy"
		}
	}
	if _, err := os.Stat(m.personPath(name)); err != nil {
		return 404, "not_found"
	}
	for _, p := range []string{filepath.Join(m.cfg.LoraDir, name+".safetensors"), m.personPath(name)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 500, "delete_failed"
		}
	}
	return 200, ""
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "t" + hex.EncodeToString(b), nil
}
