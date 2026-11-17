package train

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNameTakenByAnyLoraFile(t *testing.T) {
	for _, rel := range []string{"anna.ckpt", "anna.gguf", "sub/anna.safetensors", "Anna.safetensors"} {
		t.Run(rel, func(t *testing.T) {
			e := newEnv(t)
			p := filepath.Join(e.cfg.LoraDir, rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
			rec := e.post(personFields("anna", "woman"), photos(t, 10))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "name_taken", errorCode(t, rec))
		})
	}
	e := newEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(e.cfg.LoraDir, "annabel.safetensors"), []byte("x"), 0o644))
	e.submit("anna", "woman")
}

func TestQueuedUntilLeaseGranted(t *testing.T) {
	cfg := testConfig(t)
	granted := make(chan struct{})
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
	time.Sleep(100 * time.Millisecond)
	got := e.job(j.ID())
	assert.Equal(t, "queued", got.Status)

	require.Equal(t, http.StatusOK, e.cancel(j.ID()).Code)
	e.waitStatus(j.ID(), "cancelled")
	calls, _, _ := r.stats()
	assert.Zero(t, calls)
	close(granted)
}

func TestUploadedPhotosArePrivate(t *testing.T) {
	cfg := testConfig(t)
	e := startEnv(t, cfg, nil)
	j := e.submit("anna", "woman")
	e.waitStarted()
	st, err := os.Stat(cfg.JobsDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), st.Mode().Perm())
	files := imagesUnder(t, filepath.Join(cfg.JobsDir, j.ID()))
	require.NotEmpty(t, files)
	for _, f := range files {
		st, err := os.Stat(f)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), f)
	}
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(j.ID(), "completed")
}

func TestMissingLoraDirIsCreated(t *testing.T) {
	cfg := testConfig(t)
	cfg.LoraDir = filepath.Join(cfg.LoraDir, "new", "loras")
	e := startEnv(t, cfg, nil)
	j := e.submit("anna", "woman")
	e.waitStarted()
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(j.ID(), "completed")
	_, err := os.Stat(filepath.Join(cfg.LoraDir, "anna.safetensors"))
	assert.NoError(t, err)
}

func TestBrowserRequestsRejected(t *testing.T) {
	e := newEnv(t)
	body, ct := multipartBody(t, personFields("anna", "woman"), photos(t, 10))
	req := httptest.NewRequest(http.MethodPost, "/api/train/jobs", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, e.jobsOf(""))
}
