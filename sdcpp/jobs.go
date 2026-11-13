package sdcpp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	kindTxt2Img = "txt2img"
	kindImg2Img = "img2img"
	kindUpscale = "upscale"

	statusQueued    = "queued"
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
	statusLost      = "lost"

	maxActiveJobs = 100
	maxJobBody    = 512 << 20
)

type ModelSpec struct {
	Checkpoint     string   `json:"checkpoint"`
	VAE            string   `json:"vae"`
	Modules        []string `json:"modules"`
	KleinReference bool     `json:"klein_reference"`
	MossReference  bool     `json:"moss_reference"`
}

type HiresParams struct {
	Enable    bool     `json:"enable"`
	Scale     *float64 `json:"scale"`
	Upscaler  string   `json:"upscaler"`
	Denoising *float64 `json:"denoising"`
	Steps     int      `json:"steps"`
	ResizeX   int      `json:"resize_x"`
	ResizeY   int      `json:"resize_y"`
}

type GenParams struct {
	Prompt            string       `json:"prompt"`
	NegativePrompt    string       `json:"negative_prompt"`
	Sampler           string       `json:"sampler"`
	Scheduler         string       `json:"scheduler"`
	Steps             int          `json:"steps"`
	CfgScale          *float64     `json:"cfg_scale"`
	DistilledCfgScale *float64     `json:"distilled_cfg_scale"`
	Width             int          `json:"width"`
	Height            int          `json:"height"`
	Seed              *int64       `json:"seed"`
	ClipSkip          *int         `json:"clip_skip"`
	BatchSize         int          `json:"batch_size"`
	Hires             *HiresParams `json:"hires"`
	InitImage         string       `json:"init_image"`
	Mask              string       `json:"mask"`
	DenoisingStrength *float64     `json:"denoising_strength"`
	Image             string       `json:"image"`
	Upscaler          string       `json:"upscaler"`
	Scale             *float64     `json:"scale"`
}

type jobRequest struct {
	Kind      string     `json:"kind"`
	Client    string     `json:"client"`
	ClientRef string     `json:"client_ref"`
	Model     *ModelSpec `json:"model"`
	Params    *GenParams `json:"params"`
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type JobProgress struct {
	Step  int `json:"step"`
	Steps int `json:"steps"`
}

type JobStatus struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"`
	Client     string      `json:"client"`
	ClientRef  string      `json:"client_ref"`
	Status     string      `json:"status"`
	Position   int         `json:"position"`
	Progress   JobProgress `json:"progress"`
	Error      *JobError   `json:"error"`
	CreatedAt  *time.Time  `json:"created_at"`
	StartedAt  *time.Time  `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at"`
	ExpiresAt  *time.Time  `json:"expires_at"`
}

type jobResult struct {
	Images []string       `json:"images"`
	Info   map[string]any `json:"info"`
}

type job struct {
	st      JobStatus
	model   ModelSpec
	params  GenParams
	kind    string
	cancel  context.CancelFunc
	ctx     context.Context
	result  *jobResult
	deleted bool
}

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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("[sdcpp] write %s: %v", path, err)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
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

func (f *Facade) snapshot(j *job) JobStatus {
	f.jobs.mu.Lock()
	st := j.st
	f.jobs.mu.Unlock()
	f.mu.Lock()
	pos := f.posFn
	f.mu.Unlock()
	st.Position = 0
	if st.Status == statusQueued && pos != nil {
		st.Position = pos(st.ID)
	}
	return st
}

func (f *Facade) lookup(id string) (*job, error) {
	f.jobs.sweep()
	f.jobs.mu.Lock()
	j, ok := f.jobs.jobs[id]
	f.jobs.mu.Unlock()
	if !ok {
		return nil, apiErr(http.StatusNotFound, "not_found", "job %q not found", id)
	}
	return j, nil
}

func (f *Facade) validate(req *jobRequest) error {
	switch req.Kind {
	case kindTxt2Img, kindImg2Img, kindUpscale:
	default:
		return apiErr(http.StatusBadRequest, "invalid_params", "kind must be txt2img, img2img or upscale, got %q", req.Kind)
	}
	if req.Params == nil {
		req.Params = &GenParams{}
	}
	p := req.Params
	if req.Kind == kindUpscale {
		return f.checkUpscale(*p)
	}
	if req.Model == nil {
		return apiErr(http.StatusBadRequest, "invalid_params", "model is required for %s", req.Kind)
	}
	if !validSampler(p.Sampler) {
		return apiErr(http.StatusBadRequest, "invalid_params", "sampler %q is not an sd.cpp sampler", p.Sampler)
	}
	if !validScheduler(p.Scheduler) {
		return apiErr(http.StatusBadRequest, "invalid_params", "scheduler %q is not an sd.cpp scheduler", p.Scheduler)
	}
	if err := checkLoraWeights(p.Prompt); err != nil {
		return err
	}
	if req.Kind == kindImg2Img {
		if req.Model.MossReference {
			return apiErr(http.StatusBadRequest, "invalid_params", "правка Moss недоступна на sd.cpp — используйте Klein (Moss edit is not available on sd.cpp, use Klein)")
		}
		if p.InitImage == "" {
			return apiErr(http.StatusBadRequest, "invalid_params", "img2img needs params.init_image")
		}
		if _, err := decodeImageB64(p.InitImage); err != nil {
			return apiErr(http.StatusBadRequest, "invalid_params", "decode init_image: %v", err)
		}
		if p.Mask != "" {
			if _, err := decodeImageB64(p.Mask); err != nil {
				return apiErr(http.StatusBadRequest, "invalid_params", "decode mask: %v", err)
			}
		}
	} else if p.Hires != nil && p.Hires.Enable {
		if _, err := f.resolveUpscaler(p.Hires.Upscaler, true); err != nil {
			return err
		}
	}
	_, err := f.checkModel(*req.Model)
	return err
}

func (f *Facade) submitJob(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJobBody))
	if err != nil {
		writeErr(w, apiErr(http.StatusBadRequest, "invalid_params", "read body: %v", err))
		return
	}
	var req jobRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, apiErr(http.StatusBadRequest, "invalid_params", "invalid JSON: %v", err))
		return
	}
	req.Client = strings.TrimSpace(req.Client)
	f.jobs.sweep()
	if old := f.replayable(req.Client, req.ClientRef); old != nil {
		respondJSON(w, http.StatusOK, f.snapshot(old))
		return
	}
	if err := f.fillDefaultModel(&req); err != nil {
		writeErr(w, err)
		return
	}
	if err := f.validate(&req); err != nil {
		writeErr(w, err)
		return
	}
	s := f.jobs
	s.mu.Lock()
	if req.ClientRef != "" {
		if old, ok := s.jobs[s.refs[refKey(req.Client, req.ClientRef)]]; ok && replayStatus(old.st.Status) {
			s.mu.Unlock()
			respondJSON(w, http.StatusOK, f.snapshot(old))
			return
		}
	}
	active := 0
	for _, j := range s.jobs {
		if !terminal(j.st.Status) {
			active++
		}
	}
	if active >= maxActiveJobs {
		s.mu.Unlock()
		writeErr(w, apiErr(http.StatusTooManyRequests, "queue_full", "too many jobs waiting (%d)", active))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{
		kind:   req.Kind,
		params: *req.Params,
		ctx:    ctx,
		cancel: cancel,
		st: JobStatus{
			ID:        newJobID(),
			Kind:      req.Kind,
			Client:    req.Client,
			ClientRef: req.ClientRef,
			Status:    statusQueued,
			CreatedAt: tptr(s.now()),
		},
	}
	if req.Model != nil {
		j.model = *req.Model
	}
	s.jobs[j.st.ID] = j
	if req.ClientRef != "" {
		s.refs[refKey(req.Client, req.ClientRef)] = j.st.ID
	}
	s.mu.Unlock()
	s.persist(j)
	go f.runJob(j)
	respondJSON(w, http.StatusAccepted, f.snapshot(j))
}

func (f *Facade) fillDefaultModel(req *jobRequest) error {
	if req.Kind == kindUpscale || req.Model == nil || cleanName(req.Model.Checkpoint) != "" {
		return nil
	}
	f.mu.Lock()
	loaded, loadedName := f.loaded, f.loadedName
	f.mu.Unlock()
	f.jobs.mu.Lock()
	base, ok := f.jobs.lastQueuedModelLocked()
	f.jobs.mu.Unlock()
	if !ok && loadedName != "" {
		base, ok = loaded, true
	}
	if !ok {
		return apiErr(http.StatusConflict, "model_not_found", "no model is loaded on the server and none is queued: choose a checkpoint for the job")
	}
	req.Model.Checkpoint = base.Checkpoint
	if strings.TrimSpace(req.Model.VAE) == "" {
		req.Model.VAE = base.VAE
	}
	if len(req.Model.Modules) == 0 {
		req.Model.Modules = slices.Clone(base.Modules)
	}
	return nil
}

func replayStatus(status string) bool {
	switch status {
	case statusQueued, statusRunning, statusCompleted:
		return true
	}
	return false
}

func (f *Facade) replayable(client, ref string) *job {
	if ref == "" {
		return nil
	}
	s := f.jobs
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.jobs[s.refs[refKey(client, ref)]]; ok && replayStatus(old.st.Status) {
		return old
	}
	return nil
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

func (f *Facade) finish(j *job, status string, jerr *JobError, res *jobResult) {
	s := f.jobs
	if res != nil && s.dir != "" {
		b, werr := json.Marshal(res)
		s.pmu.Lock()
		s.mu.Lock()
		deleted := j.deleted
		s.mu.Unlock()
		if !deleted {
			if werr == nil {
				werr = os.MkdirAll(s.jobDir(j.st.ID), 0o700)
			}
			if werr == nil {
				werr = writeAtomic(filepath.Join(s.jobDir(j.st.ID), "result.json"), b)
			}
			if werr != nil {
				status, res = statusFailed, nil
				jerr = &JobError{Code: "sd_failed", Message: "could not store the result: " + werr.Error()}
			}
		}
		s.pmu.Unlock()
	}
	now := s.now()
	s.mu.Lock()
	if terminal(j.st.Status) {
		s.mu.Unlock()
		return
	}
	j.st.Status = status
	j.st.Error = jerr
	j.params = GenParams{}
	j.st.FinishedAt = tptr(now)
	j.st.ExpiresAt = tptr(now.Add(s.ttl))
	if res != nil && s.dir == "" {
		j.result = res
	}
	if s.running == j.st.ID {
		s.running = ""
	}
	s.mu.Unlock()
	s.persist(j)
}

func (f *Facade) finishErr(j *job, err error) {
	if j.ctx.Err() != nil || errors.Is(err, context.Canceled) || errorCode(err, "") == "cancelled" {
		f.finish(j, statusCancelled, &JobError{Code: "cancelled", Message: "job cancelled"}, nil)
		return
	}
	if errors.Is(err, ErrQueueFull) {
		f.finish(j, statusFailed, &JobError{Code: "queue_full", Message: err.Error()}, nil)
		return
	}
	f.finish(j, statusFailed, &JobError{Code: errorCode(err, "sd_failed"), Message: err.Error()}, nil)
}

func (f *Facade) runJob(j *job) {
	defer j.cancel()
	ctx := j.ctx
	s := f.jobs
	s.mu.Lock()
	model, params := j.model, j.params
	s.mu.Unlock()
	f.mu.Lock()
	leaser := f.leaser
	loaded := f.loadedName
	f.mu.Unlock()
	if leaser != nil {
		ckpt := model.Checkpoint
		if cleanName(ckpt) == "" {
			ckpt = loaded
		}
		release, err := leaser(ctx, LeaseRequest{JobID: j.st.ID, Client: j.st.Client, Checkpoint: ckpt})
		if err != nil {
			f.finishErr(j, err)
			return
		}
		if release != nil {
			defer release()
		}
	}
	if ctx.Err() != nil {
		f.finishErr(j, ctx.Err())
		return
	}
	select {
	case f.gen <- struct{}{}:
	case <-ctx.Done():
		f.finishErr(j, ctx.Err())
		return
	}
	defer func() { <-f.gen }()
	if ctx.Err() != nil {
		f.finishErr(j, ctx.Err())
		return
	}
	var imgs []string
	var info map[string]any
	var err error
	if j.kind == kindUpscale {
		s.markRunning(j, 0, 0)
		imgs, info, err = f.upscale(ctx, params)
	} else {
		imgs, info, err = f.generate(ctx, j, model, params)
	}
	s.mu.Lock()
	if s.running == j.st.ID {
		s.running = ""
	}
	s.mu.Unlock()
	if err != nil || ctx.Err() != nil {
		f.finishErr(j, err)
		return
	}
	f.finish(j, statusCompleted, nil, &jobResult{Images: imgs, Info: info})
}

func (f *Facade) listJobs(w http.ResponseWriter, r *http.Request) {
	f.jobs.sweep()
	client := r.URL.Query().Get("client")
	f.jobs.mu.Lock()
	list := make([]*job, 0, len(f.jobs.jobs))
	for _, j := range f.jobs.jobs {
		if client == "" || j.st.Client == client {
			list = append(list, j)
		}
	}
	f.jobs.mu.Unlock()
	out := make([]JobStatus, 0, len(list))
	for _, j := range list {
		out = append(out, f.snapshot(j))
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].CreatedAt.Equal(*out[b].CreatedAt) {
			return out[a].ID < out[b].ID
		}
		return out[a].CreatedAt.Before(*out[b].CreatedAt)
	})
	respondJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (f *Facade) getJob(w http.ResponseWriter, id string) {
	j, err := f.lookup(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, f.snapshot(j))
}

func (f *Facade) jobResult(w http.ResponseWriter, id string) {
	j, err := f.lookup(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	f.jobs.mu.Lock()
	st, res := j.st, j.result
	f.jobs.mu.Unlock()
	switch st.Status {
	case statusCompleted:
	case statusQueued, statusRunning:
		writeErr(w, apiErr(http.StatusConflict, "not_ready", "job %s is %s", id, st.Status))
		return
	default:
		code, msg := "not_ready", "job "+id+" is "+st.Status
		if st.Error != nil {
			code, msg = st.Error.Code, st.Error.Message
		}
		writeErr(w, apiErr(http.StatusConflict, code, "%s", msg))
		return
	}
	if res == nil {
		b, err := os.ReadFile(filepath.Join(f.jobs.jobDir(id), "result.json"))
		var r jobResult
		if err != nil || json.Unmarshal(b, &r) != nil {
			writeErr(w, apiErr(http.StatusNotFound, "not_found", "result of job %s is gone", id))
			return
		}
		res = &r
	}
	images, info := res.Images, res.Info
	if images == nil {
		images = []string{}
	}
	if info == nil {
		info = map[string]any{}
	}
	respondJSON(w, http.StatusOK, map[string]any{"images": images, "info": info})
}

func (f *Facade) CancelJob(id string) bool {
	f.jobs.mu.Lock()
	j, ok := f.jobs.jobs[id]
	f.jobs.mu.Unlock()
	if !ok {
		return false
	}
	j.stop()
	return true
}

func (j *job) stop() {
	if j.cancel != nil {
		j.cancel()
	}
}

func (s *jobStore) janitor() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.sweep()
	}
}

func (f *Facade) cancelJobHTTP(w http.ResponseWriter, id string) {
	j, err := f.lookup(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	f.jobs.mu.Lock()
	queued := j.st.Status == statusQueued
	f.jobs.mu.Unlock()
	if queued {
		f.finish(j, statusCancelled, &JobError{Code: "cancelled", Message: "job cancelled"}, nil)
	}
	j.stop()
	respondJSON(w, http.StatusOK, f.snapshot(j))
}

func (f *Facade) deleteJob(w http.ResponseWriter, id string) {
	if _, err := f.lookup(id); err != nil {
		writeErr(w, err)
		return
	}
	s := f.jobs
	s.mu.Lock()
	j, ok := s.jobs[id]
	if ok {
		s.dropLocked(id, j)
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, apiErr(http.StatusNotFound, "not_found", "job %q not found", id))
		return
	}
	j.stop()
	s.removeFiles(id)
	w.WriteHeader(http.StatusNoContent)
}
