package sdcpp

import (
	"context"
	"encoding/json"
	"imagecaster-server/imgb64"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

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
	if err := checkCache(p.CacheMode, p.CacheOption); err != nil {
		return err
	}
	if req.Kind == kindImg2Img {
		if req.Model.MossReference {
			return apiErr(http.StatusBadRequest, "invalid_params", "правка Moss недоступна на sd.cpp — используйте Klein (Moss edit is not available on sd.cpp, use Klein)")
		}
		if p.InitImage == "" {
			return apiErr(http.StatusBadRequest, "invalid_params", "img2img needs params.init_image")
		}
		if _, _, err := imgb64.Check(p.InitImage, imageLimits); err != nil {
			return apiErr(http.StatusBadRequest, "invalid_params", "decode init_image: %v", err)
		}
		if p.Mask != "" {
			if _, _, err := imgb64.Check(p.Mask, imageLimits); err != nil {
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
	id := newJobID()
	params := *req.Params
	stashed := s.stashInputs(id, &params)
	s.mu.Lock()
	if req.ClientRef != "" {
		if old, ok := s.jobs[s.refs[refKey(req.Client, req.ClientRef)]]; ok && replayStatus(old.st.Status) {
			s.mu.Unlock()
			s.discardStash(id, stashed)
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
		s.discardStash(id, stashed)
		writeErr(w, apiErr(http.StatusTooManyRequests, "queue_full", "too many jobs waiting (%d)", active))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{
		kind:    req.Kind,
		params:  params,
		stashed: stashed,
		ctx:     ctx,
		cancel:  cancel,
		st: JobStatus{
			ID:        id,
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
	if res != nil {
		respondJSON(w, http.StatusOK, res.normalized())
		return
	}
	path := filepath.Join(f.jobs.jobDir(id), "result.json")
	fh, err := os.Open(path)
	if err != nil {
		writeErr(w, apiErr(http.StatusNotFound, "not_found", "result of job %s is gone", id))
		return
	}
	defer fh.Close()
	st2, err := fh.Stat()
	if err != nil || !resultNormalized(fh, st2.Size()) {
		b, rerr := os.ReadFile(path)
		var r jobResult
		if err != nil || rerr != nil || json.Unmarshal(b, &r) != nil {
			writeErr(w, apiErr(http.StatusNotFound, "not_found", "result of job %s is gone", id))
			return
		}
		respondJSON(w, http.StatusOK, r.normalized())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.FormatInt(st2.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, fh); err != nil {
		log.Printf("[sdcpp] job %s: send result: %v", id, err)
	}
}

func (r *jobResult) normalized() *jobResult {
	out := *r
	if out.Images == nil {
		out.Images = []string{}
	}
	if out.Info == nil {
		out.Info = map[string]any{}
	}
	return &out
}

const (
	resultHead = `{"images":[`
	resultTail = `}}`
)

func resultNormalized(f io.ReaderAt, size int64) bool {
	if size < int64(len(resultHead)+len(resultTail)) {
		return false
	}
	head := make([]byte, len(resultHead))
	tail := make([]byte, len(resultTail))
	if _, err := f.ReadAt(head, 0); err != nil {
		return false
	}
	if _, err := f.ReadAt(tail, size-int64(len(tail))); err != nil {
		return false
	}
	return string(head) == resultHead && string(tail) == resultTail
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
