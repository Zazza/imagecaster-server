package train

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func (e *env) pause(id string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/train/jobs/"+id+"/pause", nil, "")
}

func (e *env) resume(id string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/train/jobs/"+id+"/resume", nil, "")
}

func (e *env) rename(old, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/train/people/"+old+"/rename", strings.NewReader(body), "application/json")
}

func (e *env) finishedAt(id string) any {
	e.t.Helper()
	rec := e.get("/api/train/jobs/" + id)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	var raw map[string]any
	require.NoError(e.t, json.Unmarshal(rec.Body.Bytes(), &raw))
	v, ok := raw["finished_at"]
	if !ok {
		return nil
	}
	return v
}

func (e *env) pauseRunning(id string) {
	e.t.Helper()
	rec := e.pause(id)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(e.t, id, decodeJob(e.t, rec).ID())
	e.waitStatus(id, "paused")
}

func writeCheckpoint(t *testing.T, spec RunSpec) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(spec.OutputPath), "checkpoint_000000250.safetensors")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("ckpt"), 0o644))
	return p
}

func writePerson(t *testing.T, loraDir, name, gender, base string, steps int) {
	t.Helper()
	now := time.Now()
	p := Person{Name: name, Trigger: name + "_" + gender, Gender: gender, Base: base, Lora: name, Steps: steps, CreatedAt: &now}
	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(loraDir, name+".safetensors"), []byte("lora-"+name), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(loraDir, name+".person.json"), data, 0o644))
}

func waitReturns(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("call did not return")
	}
}

func TestPause_RunningJobKeepsFiles(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	call := e.waitStarted()
	ckpt := writeCheckpoint(t, call.spec)

	e.pauseRunning(j.ID())

	_, _, cancelled := e.runner.stats()
	assert.Equal(t, 1, cancelled)
	assert.Nil(t, e.finishedAt(j.ID()))
	assert.Equal(t, 10, countImages(call.spec.DatasetDir))
	assert.FileExists(t, ckpt)
	assert.NoFileExists(t, e.loraPath("anna"))
}

func TestPause_AlreadyPaused(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()
	e.pauseRunning(j.ID())

	rec := e.pause(j.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "paused", decodeJob(t, rec).Status)
	assert.Equal(t, "paused", e.job(j.ID()).Status)
}

func TestPause_QueuedNotRunning(t *testing.T) {
	e := newEnv(t)
	e.submit("anna", "woman")
	e.waitStarted()
	second := e.submit("boris", "man")
	e.waitStatus(second.ID(), "queued")

	rec := e.pause(second.ID())
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "not_running", errorCode(t, rec))
	assert.Equal(t, "queued", e.job(second.ID()).Status)
}

func TestPause_FinishedNotRunning(t *testing.T) {
	tests := []struct {
		name   string
		end    func(e *env, id string)
		status string
	}{
		{"completed", func(e *env, id string) { e.finishRun(runResult{writeOutput: true}) }, "completed"},
		{"failed", func(e *env, id string) { e.finishRun(runResult{err: errors.New("cuda out of memory")}) }, "failed"},
		{"cancelled", func(e *env, id string) {
			require.Equal(e.t, http.StatusOK, e.cancel(id).Code)
		}, "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			j := e.submit("anna", "woman")
			e.waitStarted()
			tt.end(e, j.ID())
			e.waitStatus(j.ID(), tt.status)

			rec := e.pause(j.ID())
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "not_running", errorCode(t, rec))
			assert.Equal(t, tt.status, e.job(j.ID()).Status)
		})
	}
}

func TestPause_UnknownJob(t *testing.T) {
	e := newEnv(t)
	rec := e.pause("missing")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "not_found", errorCode(t, rec))
}

func TestPause_WaitDoesNotBlock(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()
	e.pauseRunning(j.ID())

	waitReturns(t, e.m.Wait)
	assert.Equal(t, "paused", e.job(j.ID()).Status)
}

func TestPause_HoldsName(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()
	e.pauseRunning(j.ID())

	rec := e.post(personFields("anna", "woman"), photos(t, 10))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "name_taken", errorCode(t, rec))
}

func TestPause_CancelPausedRemovesFiles(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	call := e.waitStarted()
	ckpt := writeCheckpoint(t, call.spec)
	e.pauseRunning(j.ID())

	rec := e.cancel(j.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	e.waitStatus(j.ID(), "cancelled")

	assert.NotNil(t, e.finishedAt(j.ID()))
	assertGone(t, call.spec.DatasetDir)
	assertGone(t, ckpt)
	assert.Empty(t, imagesUnder(t, e.cfg.JobsDir))
	calls, _, _ := e.runner.stats()
	assert.Equal(t, 1, calls)
}

func TestPause_LeaseLostStillFails(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()

	e.m.CancelRunning()
	got := e.waitStatus(j.ID(), "failed")
	require.NotNil(t, got.Error)
	assert.Equal(t, "gpu_lease_lost", got.Error.Code)
}

func TestResume_RunsAgainInSameDir(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	first := e.waitStarted()
	ckpt := writeCheckpoint(t, first.spec)
	e.pauseRunning(j.ID())

	rec := e.resume(j.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, j.ID(), decodeJob(t, rec).ID())

	second := e.waitStarted()
	assert.Equal(t, first.spec.WorkDir, second.spec.WorkDir)
	assert.Equal(t, first.spec.OutputPath, second.spec.OutputPath)
	assert.Equal(t, 10, second.photos)
	assert.FileExists(t, ckpt)
	e.waitStatus(j.ID(), "running")

	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(j.ID(), "completed")
	assert.FileExists(t, e.loraPath("anna"))
	calls, _, _ := e.runner.stats()
	assert.Equal(t, 2, calls)
}

func TestResume_StatusQueued(t *testing.T) {
	cfg := testConfig(t)
	granted := make(chan struct{}, 1)
	granted <- struct{}{}
	r := newFakeRunner(&eventLog{})
	lease := func(ctx context.Context, client string) (func(), error) {
		select {
		case <-granted:
			return func() {}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m, err := New(cfg, r, lease)
	require.NoError(t, err)
	t.Cleanup(m.Close)
	t.Cleanup(r.stop)
	e := &env{t: t, cfg: cfg, m: m, runner: r, log: &eventLog{}}

	j := e.submit("anna", "woman")
	e.waitStarted()
	e.pauseRunning(j.ID())

	rec := e.resume(j.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "queued", decodeJob(t, rec).Status)
	assert.Equal(t, "queued", e.job(j.ID()).Status)
}

func TestResume_WaitsForBusyGPU(t *testing.T) {
	e := newEnv(t)
	a := e.submit("anna", "woman")
	e.waitStarted()
	e.pauseRunning(a.ID())

	b := e.submit("boris", "man")
	e.waitStarted()
	require.Equal(t, http.StatusOK, e.resume(a.ID()).Code)
	e.waitStatus(a.ID(), "queued")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "queued", e.job(a.ID()).Status)
	calls, _, _ := e.runner.stats()
	assert.Equal(t, 2, calls)

	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(b.ID(), "completed")
	e.waitStarted()
	e.waitStatus(a.ID(), "running")
	_, maxActive, _ := e.runner.stats()
	assert.Equal(t, 1, maxActive)
}

type resumeRunner struct {
	mu    sync.Mutex
	n     int
	calls chan RunSpec
}

func (r *resumeRunner) Run(ctx context.Context, spec RunSpec, progress func(step, steps int)) error {
	r.mu.Lock()
	r.n++
	n := r.n
	r.mu.Unlock()
	if n == 1 {
		progress(7, 10)
	}
	r.calls <- spec
	<-ctx.Done()
	return ctx.Err()
}

func TestResume_KeepsProgressStep(t *testing.T) {
	cfg := testConfig(t)
	r := &resumeRunner{calls: make(chan RunSpec, 4)}
	m, err := New(cfg, r, nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)
	e := &env{t: t, cfg: cfg, m: m, log: &eventLog{}}
	next := func() {
		select {
		case <-r.calls:
		case <-time.After(waitTimeout):
			t.Fatal("runner was not started")
		}
	}

	j := e.submit("anna", "woman")
	next()
	e.waitJob(j.ID(), "step 7", func(v jobView) bool { return v.Progress.Step == 7 })
	e.pauseRunning(j.ID())
	assert.Equal(t, 7, e.job(j.ID()).Progress.Step)

	require.Equal(t, http.StatusOK, e.resume(j.ID()).Code)
	next()
	got := e.waitStatus(j.ID(), "running")
	assert.Equal(t, 7, got.Progress.Step)
}

func TestResume_NoopForQueuedAndRunning(t *testing.T) {
	e := newEnv(t)
	first := e.submit("anna", "woman")
	e.waitStarted()
	second := e.submit("boris", "man")
	e.waitStatus(second.ID(), "queued")

	rec := e.resume(first.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "running", e.job(first.ID()).Status)

	rec = e.resume(second.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "queued", e.job(second.ID()).Status)

	calls, _, _ := e.runner.stats()
	assert.Equal(t, 1, calls)
}

func TestResume_FinishedNotPaused(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(j.ID(), "completed")

	rec := e.resume(j.ID())
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "not_paused", errorCode(t, rec))
	assert.Equal(t, "completed", e.job(j.ID()).Status)
}

func TestResume_UnknownJob(t *testing.T) {
	e := newEnv(t)
	rec := e.resume("missing")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "not_found", errorCode(t, rec))
}

func TestRestore_ShutdownPausesRunningJob(t *testing.T) {
	cfg := testConfig(t)
	before := startEnv(t, cfg, nil)
	j := before.submit("anna", "woman")
	first := before.waitStarted()
	ckpt := writeCheckpoint(t, first.spec)

	waitReturns(t, before.m.Close)

	data, err := os.ReadFile(filepath.Join(cfg.JobsDir, j.ID(), statusFile))
	require.NoError(t, err)
	var saved struct {
		Status     string     `json:"status"`
		FinishedAt *time.Time `json:"finished_at"`
	}
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "paused", saved.Status)
	assert.Nil(t, saved.FinishedAt)
	assert.Equal(t, 10, countImages(first.spec.DatasetDir))
	assert.FileExists(t, ckpt)

	after := startEnv(t, cfg, nil)
	assert.Equal(t, "paused", after.job(j.ID()).Status)
	assert.Equal(t, 10, countImages(first.spec.DatasetDir))
	assert.FileExists(t, ckpt)

	require.Equal(t, http.StatusOK, after.resume(j.ID()).Code)
	second := after.waitStarted()
	assert.Equal(t, first.spec.WorkDir, second.spec.WorkDir)
	assert.Equal(t, 10, second.photos)
}

func writeStoredJob(t *testing.T, cfg Config, id, name, status string, created time.Time) string {
	t.Helper()
	dir := filepath.Join(cfg.JobsDir, id)
	ds := filepath.Join(dir, datasetDir)
	require.NoError(t, os.MkdirAll(ds, 0o700))
	img := filepath.Join(ds, "001.png")
	require.NoError(t, os.WriteFile(img, pngBytes(t, 1), 0o600))
	started := created.Add(time.Minute)
	j := Job{
		ID: id, Client: testClient, Name: name, Trigger: name + "_woman", Gender: "woman", Base: baseZImage,
		Photos: 10, Status: status, Progress: Progress{Step: 500, Steps: cfg.Steps}, CreatedAt: &created, StartedAt: &started,
	}
	data, err := json.Marshal(j)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, statusFile), data, 0o644))
	return img
}

func TestRestore_CrashedJobBecomesPaused(t *testing.T) {
	for _, status := range []string{"running", "queued"} {
		t.Run(status, func(t *testing.T) {
			cfg := testConfig(t)
			img := writeStoredJob(t, cfg, "tcrash0001", "anna", status, time.Now().Add(-time.Hour))

			e := startEnv(t, cfg, nil)
			got := e.job("tcrash0001")
			assert.Equal(t, "paused", got.Status)
			assert.Nil(t, got.Error)
			assert.Nil(t, e.finishedAt("tcrash0001"))
			assert.FileExists(t, img)

			time.Sleep(100 * time.Millisecond)
			calls, _, _ := e.runner.stats()
			assert.Zero(t, calls)

			require.Equal(t, http.StatusOK, e.resume("tcrash0001").Code)
			call := e.waitStarted()
			assert.Equal(t, filepath.Join(cfg.JobsDir, "tcrash0001"), call.spec.WorkDir)
		})
	}
}

func TestRestore_PausedJobSurvivesTTL(t *testing.T) {
	cfg := testConfig(t)
	img := writeStoredJob(t, cfg, "told000001", "anna", "paused", time.Now().Add(-30*24*time.Hour))

	e := startEnv(t, cfg, nil)
	assert.Equal(t, "paused", e.job("told000001").Status)
	assert.FileExists(t, img)
}

func TestSaveEvery(t *testing.T) {
	tests := []struct {
		steps int
		want  int
	}{
		{2000, 250},
		{100, 100},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("steps=%d", tt.steps), func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Steps = tt.steps
			e := startEnv(t, cfg, nil)
			e.submit("anna", "woman")
			call := e.waitStarted()

			data, err := os.ReadFile(call.spec.ConfigPath)
			require.NoError(t, err)
			var doc struct {
				Config struct {
					Process []struct {
						Save struct {
							SaveEvery int `yaml:"save_every"`
							KeepSaves int `yaml:"max_step_saves_to_keep"`
						} `yaml:"save"`
						Train struct {
							Steps int `yaml:"steps"`
						} `yaml:"train"`
					} `yaml:"process"`
				} `yaml:"config"`
			}
			require.NoError(t, yaml.Unmarshal(data, &doc))
			require.Len(t, doc.Config.Process, 1)
			assert.Equal(t, tt.want, doc.Config.Process[0].Save.SaveEvery)
			assert.Equal(t, 1, doc.Config.Process[0].Save.KeepSaves)
			assert.Equal(t, tt.steps, doc.Config.Process[0].Train.Steps)
		})
	}
}

func TestRename_Success(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", "Moss", 1500)

	rec := e.rename("anna", `{"name":"maria"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Person personView `json:"person"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	assert.Equal(t, "maria", resp.Person.Name)
	assert.Equal(t, "maria", resp.Person.Lora)

	assert.FileExists(t, e.loraPath("maria"))
	assert.FileExists(t, filepath.Join(e.cfg.LoraDir, "maria.person.json"))
	assert.NoFileExists(t, e.loraPath("anna"))
	assert.NoFileExists(t, filepath.Join(e.cfg.LoraDir, "anna.person.json"))
	data, err := os.ReadFile(e.loraPath("maria"))
	require.NoError(t, err)
	assert.Equal(t, "lora-anna", string(data))

	people := e.people()
	require.Len(t, people, 1)
	assert.Equal(t, "maria", people[0].Name)
	assert.Equal(t, "maria", people[0].Lora)
	assert.Equal(t, "woman", people[0].Gender)
	assert.Equal(t, "Moss", people[0].Base)
	assert.Equal(t, 1500, people[0].Steps)
}

func TestRename_NormalizesName(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

	rec := e.rename("anna", `{"name":" New Name "}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.FileExists(t, e.loraPath("new_name"))
	people := e.people()
	require.Len(t, people, 1)
	assert.Equal(t, "new_name", people[0].Name)

	rec = e.rename("new_name", `{"name":"my-girl"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.FileExists(t, e.loraPath("my_girl"))
}

func TestRename_InvalidName(t *testing.T) {
	for _, name := range []string{"", "   ", "a", "bad!name", strings.Repeat("a", 33)} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
			body, err := json.Marshal(map[string]string{"name": name})
			require.NoError(t, err)

			rec := e.rename("anna", string(body))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_request", errorCode(t, rec))
			assert.FileExists(t, e.loraPath("anna"))
			assert.FileExists(t, filepath.Join(e.cfg.LoraDir, "anna.person.json"))
		})
	}
}

func TestRename_BadJSON(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

	rec := e.rename("anna", `{"name":`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "invalid_request", errorCode(t, rec))
	assert.FileExists(t, e.loraPath("anna"))
}

func assertRenameTaken(t *testing.T, e *env, newName string) {
	t.Helper()
	rec := e.rename("anna", `{"name":"`+newName+`"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "name_taken", errorCode(t, rec))
	assert.FileExists(t, e.loraPath("anna"))
	assert.FileExists(t, filepath.Join(e.cfg.LoraDir, "anna.person.json"))
}

func TestRename_TakenByPerson(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	writePerson(t, e.cfg.LoraDir, "maria", "woman", baseZImage, 2000)

	assertRenameTaken(t, e, "maria")
	data, err := os.ReadFile(e.loraPath("maria"))
	require.NoError(t, err)
	assert.Equal(t, "lora-maria", string(data))
}

func TestRename_TakenByLoraFile(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	require.NoError(t, os.WriteFile(e.loraPath("style"), []byte("x"), 0o644))

	assertRenameTaken(t, e, "style")
	data, err := os.ReadFile(e.loraPath("style"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(data))
}

func TestRename_TakenByJob(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		e := newEnv(t)
		writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
		e.submit("boris", "man")
		e.waitStarted()
		assertRenameTaken(t, e, "boris")
	})
	t.Run("queued", func(t *testing.T) {
		e := newEnv(t)
		writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
		e.submit("carl", "man")
		e.waitStarted()
		q := e.submit("boris", "man")
		e.waitStatus(q.ID(), "queued")
		assertRenameTaken(t, e, "boris")
	})
	t.Run("paused", func(t *testing.T) {
		e := newEnv(t)
		writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
		j := e.submit("boris", "man")
		e.waitStarted()
		e.pauseRunning(j.ID())
		assertRenameTaken(t, e, "boris")
	})
}

func TestRename_SameName(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	before, err := os.ReadFile(filepath.Join(e.cfg.LoraDir, "anna.person.json"))
	require.NoError(t, err)

	rec := e.rename("anna", `{"name":"anna"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.FileExists(t, e.loraPath("anna"))
	after, err := os.ReadFile(filepath.Join(e.cfg.LoraDir, "anna.person.json"))
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
	people := e.people()
	require.Len(t, people, 1)
	assert.Equal(t, "anna", people[0].Name)
}

func TestRename_UnknownPerson(t *testing.T) {
	e := newEnv(t)
	rec := e.rename("nobody", `{"name":"maria"}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "not_found", errorCode(t, rec))
	assert.NoFileExists(t, e.loraPath("maria"))
}

func TestRename_BrowserRejected(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	req := httptest.NewRequest(http.MethodPost, "/api/train/people/anna/rename", strings.NewReader(`{"name":"maria"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.FileExists(t, e.loraPath("anna"))
	assert.NoFileExists(t, e.loraPath("maria"))
}
