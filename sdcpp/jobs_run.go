package sdcpp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (f *Facade) finish(j *job, status string, jerr *JobError, res *jobResult) {
	s := f.jobs
	if res != nil {
		res = res.normalized()
	}
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
	s.dropInputs(j)
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
	if j.kind == kindFaceSwap {
		f.runFaceSwap(j, params)
		return
	}
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
	if err := s.loadInputs(j, &params); err != nil {
		f.finishErr(j, err)
		return
	}
	var imgs []string
	var info map[string]any
	var err error
	if j.kind == kindUpscale {
		s.markRunning(j, 0, 0)
		imgs, info, err = f.upscale(ctx, params)
	} else {
		imgs, info, err = f.generate(ctx, j, model, params.withDefaults())
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
