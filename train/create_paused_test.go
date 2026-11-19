package train

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const heldIdle = 200 * time.Millisecond

func heldFields(name, gender string, extra map[string]string) map[string]string {
	f := personFields(name, gender)
	f["start"] = "false"
	for k, v := range extra {
		f[k] = v
	}
	return f
}

func (e *env) submitHeld(name, gender string, extra map[string]string) jobView {
	e.t.Helper()
	rec := e.post(heldFields(name, gender, extra), photos(e.t, 10))
	require.Equal(e.t, http.StatusAccepted, rec.Code, rec.Body.String())
	return decodeJob(e.t, rec)
}

func rawField(t *testing.T, rec *httptest.ResponseRecorder, key string) (any, bool) {
	t.Helper()
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw), rec.Body.String())
	v, ok := raw[key]
	return v, ok
}

func (e *env) jobPhotos(id string) int {
	e.t.Helper()
	return len(imagesUnder(e.t, filepath.Join(e.cfg.JobsDir, id)))
}

func (e *env) assertNotRun(id string) {
	e.t.Helper()
	time.Sleep(heldIdle)
	calls, _, _ := e.runner.stats()
	assert.Zero(e.t, calls)
	assert.Equal(e.t, "paused", e.job(id).Status)
}

func TestCreatePaused_HeldJobIsPausedAndNotRun(t *testing.T) {
	e := newEnv(t)
	rec := e.post(heldFields("anna", "woman", nil), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := decodeJob(t, rec)

	assert.Equal(t, "paused", j.Status)
	assert.Equal(t, 0, j.Progress.Step)
	assert.Equal(t, e.cfg.Steps, j.Progress.Steps)
	assert.Nil(t, j.Error)
	started, _ := rawField(t, rec, "started_at")
	assert.Nil(t, started)

	e.assertNotRun(j.ID())

	got := e.get("/api/train/jobs/" + j.ID())
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	started, _ = rawField(t, got, "started_at")
	assert.Nil(t, started)
	assert.Nil(t, e.finishedAt(j.ID()))
	assert.Equal(t, 0, e.job(j.ID()).Progress.Step)
	assert.Equal(t, 10, e.jobPhotos(j.ID()))
}

func TestCreatePaused_OtherStartValuesRunAsBefore(t *testing.T) {
	tests := []struct {
		name  string
		start *string
	}{
		{"absent", nil},
		{"true", ptr("true")},
		{"empty", ptr("")},
		{"one", ptr("1")},
		{"zero", ptr("0")},
		{"no", ptr("no")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			fields := personFields("anna", "woman")
			if tt.start != nil {
				fields["start"] = *tt.start
			}
			rec := e.post(fields, photos(t, 10))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			j := decodeJob(t, rec)
			assert.Contains(t, []string{"queued", "running"}, j.Status)

			e.waitStarted()
			e.finishRun(runResult{writeOutput: true})
			e.waitStatus(j.ID(), "completed")
			assert.FileExists(t, e.loraPath("anna"))
		})
	}
}

func ptr(s string) *string { return &s }

func TestCreatePaused_ResumeRunsToCompleted(t *testing.T) {
	e := newEnv(t)
	j := e.submitHeld("anna", "woman", nil)
	e.assertNotRun(j.ID())

	rec := e.resume(j.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, j.ID(), decodeJob(t, rec).ID())
	assert.Contains(t, []string{"queued", "running"}, decodeJob(t, rec).Status)

	call := e.waitStarted()
	assert.Equal(t, 10, call.photos)
	e.waitStatus(j.ID(), "running")
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(j.ID(), "completed")
	assert.FileExists(t, e.loraPath("anna"))
	calls, maxActive, _ := e.runner.stats()
	assert.Equal(t, 1, calls)
	assert.Equal(t, 1, maxActive)
}

func TestCreatePaused_ResumeWhileOtherRunsWaitsInQueue(t *testing.T) {
	e := newEnv(t)
	held := e.submitHeld("anna", "woman", nil)
	other := e.submit("boris", "man")
	e.waitStarted()

	require.Equal(t, http.StatusOK, e.resume(held.ID()).Code)
	e.waitStatus(held.ID(), "queued")
	time.Sleep(heldIdle)
	assert.Equal(t, "queued", e.job(held.ID()).Status)

	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(other.ID(), "completed")
	e.waitStarted()
	e.waitStatus(held.ID(), "running")
	_, maxActive, _ := e.runner.stats()
	assert.Equal(t, 1, maxActive)
}

func TestCreatePaused_TwoHeldJobsRunOneAtATime(t *testing.T) {
	e := newEnv(t)
	a := e.submitHeld("anna", "woman", nil)
	b := e.submitHeld("boris", "man", nil)
	assert.Equal(t, "paused", e.job(a.ID()).Status)
	assert.Equal(t, "paused", e.job(b.ID()).Status)
	time.Sleep(heldIdle)
	calls, _, _ := e.runner.stats()
	assert.Zero(t, calls)

	require.Equal(t, http.StatusOK, e.resume(a.ID()).Code)
	require.Equal(t, http.StatusOK, e.resume(b.ID()).Code)

	first := e.waitStarted()
	time.Sleep(heldIdle)
	calls, _, _ = e.runner.stats()
	assert.Equal(t, 1, calls)
	statuses := []string{e.job(a.ID()).Status, e.job(b.ID()).Status}
	assert.ElementsMatch(t, []string{"running", "queued"}, statuses)

	e.finishRun(runResult{writeOutput: true})
	second := e.waitStarted()
	assert.NotEqual(t, first.spec.WorkDir, second.spec.WorkDir)
	e.finishRun(runResult{writeOutput: true})

	e.waitStatus(a.ID(), "completed")
	e.waitStatus(b.ID(), "completed")
	calls, maxActive, _ := e.runner.stats()
	assert.Equal(t, 2, calls)
	assert.Equal(t, 1, maxActive)
	assert.FileExists(t, e.loraPath("anna"))
	assert.FileExists(t, e.loraPath("boris"))
}

func TestCreatePaused_SurvivesRestart(t *testing.T) {
	cfg := testConfig(t)
	before := startEnv(t, cfg, nil)
	j := before.submitHeld("anna", "woman", nil)
	require.Equal(t, 10, before.jobPhotos(j.ID()))

	waitReturns(t, before.m.Close)

	after := startEnv(t, cfg, nil)
	rec := after.get("/api/train/jobs/" + j.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeJob(t, rec)
	assert.Equal(t, "paused", got.Status)
	assert.Equal(t, "anna", got.Name)
	assert.Equal(t, 0, got.Progress.Step)
	started, _ := rawField(t, rec, "started_at")
	assert.Nil(t, started)
	assert.Equal(t, 10, after.jobPhotos(j.ID()))
	after.assertNotRun(j.ID())

	require.Equal(t, http.StatusOK, after.resume(j.ID()).Code)
	call := after.waitStarted()
	assert.Equal(t, 10, call.photos)
}

func TestCreatePaused_CancelRemovesPhotos(t *testing.T) {
	e := newEnv(t)
	j := e.submitHeld("anna", "woman", nil)
	require.Equal(t, 10, e.jobPhotos(j.ID()))

	rec := e.cancel(j.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	e.waitStatus(j.ID(), "cancelled")

	assert.NotNil(t, e.finishedAt(j.ID()))
	assert.Eventually(t, func() bool {
		return len(imagesUnder(t, e.cfg.JobsDir)) == 0
	}, waitTimeout, 10*time.Millisecond)
	calls, _, _ := e.runner.stats()
	assert.Zero(t, calls)
}

func TestCreatePaused_ResubmitSameRef(t *testing.T) {
	tests := []struct {
		name  string
		start *string
	}{
		{"held again", ptr("false")},
		{"without start", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			j := e.submitHeld("anna", "woman", map[string]string{"client_ref": "ref-1"})

			fields := personFields("anna", "woman")
			fields["client_ref"] = "ref-1"
			if tt.start != nil {
				fields["start"] = *tt.start
			}
			rec := e.post(fields, photos(t, 10))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			again := decodeJob(t, rec)
			assert.Equal(t, j.ID(), again.ID())
			assert.Equal(t, "paused", again.Status)
			assert.Len(t, e.jobsOf(testClient), 1)
			e.assertNotRun(j.ID())
		})
	}
}

func TestCreatePaused_HoldsName(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]string
	}{
		{"other client_ref", heldFields("anna", "woman", map[string]string{"client_ref": "ref-2"})},
		{"other client_ref, start", func() map[string]string {
			f := personFields("anna", "woman")
			f["client_ref"] = "ref-2"
			return f
		}()},
		{"no client_ref", personFields("anna", "woman")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			j := e.submitHeld("anna", "woman", map[string]string{"client_ref": "ref-1"})

			rec := e.post(tt.fields, photos(t, 10))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "name_taken", errorCode(t, rec))
			assert.Len(t, e.jobsOf(testClient), 1)
			assert.Equal(t, "paused", e.job(j.ID()).Status)
		})
	}
}
