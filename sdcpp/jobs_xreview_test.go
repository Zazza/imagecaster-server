package sdcpp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type xrLeaser struct {
	mu    sync.Mutex
	reqs  []LeaseRequest
	block map[int]chan struct{}
}

func newXrLeaser() *xrLeaser {
	return &xrLeaser{block: map[int]chan struct{}{}}
}

func (l *xrLeaser) blockCall(n int) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch := make(chan struct{})
	l.block[n] = ch
	return ch
}

func (l *xrLeaser) lease(ctx context.Context, req LeaseRequest) (func(), error) {
	l.mu.Lock()
	l.reqs = append(l.reqs, req)
	ch := l.block[len(l.reqs)]
	l.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return func() {}, nil
}

func (l *xrLeaser) requests() []LeaseRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LeaseRequest(nil), l.reqs...)
}

func (l *xrLeaser) waitCount(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(l.requests()) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("leaser got %d requests, want %d", len(l.requests()), n)
}

func xrJobs(t *testing.T, e *testEnv) []map[string]any {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/jobs", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Jobs []map[string]any `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Jobs
}

func xrErr(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	return sub(t, m, "error")
}

func TestXReviewResultStoreFailureFailsJob(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	cases := []struct {
		name   string
		break_ func(t *testing.T, jobsDir, jobDir string)
	}{
		{"read-only store dirs", func(t *testing.T, jobsDir, jobDir string) {
			if fi, err := os.Stat(jobDir); err == nil && fi.IsDir() {
				require.NoError(t, os.Chmod(jobDir, 0o500))
			}
			require.NoError(t, os.Chmod(jobsDir, 0o500))
		}},
		{"file where job dir should be", func(t *testing.T, jobsDir, jobDir string) {
			require.NoError(t, os.RemoveAll(jobDir))
			require.NoError(t, os.WriteFile(jobDir, []byte("x"), 0o600))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jobsDir := filepath.Join(t.TempDir(), "sdjobs")
			e := newEnv(t, func(c *Config) { c.JobsDir = jobsDir })
			l := newXrLeaser()
			gate := l.blockCall(1)
			e.f.SetLeaser(l.lease)

			code, st := e.submit(t, txt(map[string]any{"prompt": "p"}))
			require.Equal(t, http.StatusAccepted, code, st)
			id := st["id"].(string)
			l.waitCount(t, 1)

			jobDir := filepath.Join(jobsDir, id)
			t.Cleanup(func() {
				_ = os.Chmod(jobsDir, 0o700)
				_ = os.Chmod(jobDir, 0o700)
			})
			tc.break_(t, jobsDir, jobDir)
			close(gate)

			final := e.wait(t, id)
			assert.Equal(t, statusFailed, final["status"], final)
			jerr := xrErr(t, final)
			assert.Equal(t, "sd_failed", jerr["code"])
			assert.Contains(t, jerr["message"], "could not store the result")
		})
	}
}

func TestXReviewClientRefReplayBeforeValidation(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(t *testing.T, e *testEnv, body map[string]any)
	}{
		{"checkpoint removed from models dir", func(t *testing.T, e *testEnv, body map[string]any) {
			err := os.Remove(filepath.Join(e.cfg.ModelsDir, "sdxl_base.safetensors"))
			if !os.IsNotExist(err) {
				require.NoError(t, err)
			}
		}},
		{"invalid sampler", func(t *testing.T, e *testEnv, body map[string]any) {
			body["params"].(map[string]any)["sampler"] = "DPM++ 2M Karras"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			first := txt(map[string]any{"prompt": "p"})
			first["client_ref"] = "ref-1"
			st, _, _ := e.run(t, first)
			require.Equal(t, statusCompleted, st["status"], st)
			id := st["id"].(string)

			again := txt(map[string]any{"prompt": "p"})
			again["client_ref"] = "ref-1"
			tc.break_(t, e, again)

			code, got := e.submit(t, again)
			assert.Equal(t, http.StatusOK, code, got)
			assert.Equal(t, id, got["id"])
			assert.Equal(t, statusCompleted, got["status"])
			assert.Len(t, xrJobs(t, e), 1)

			other := txt(map[string]any{"prompt": "p"})
			other["client_ref"] = "ref-1"
			other["client"] = "someone-else"
			tc.break_(t, e, other)
			code, got = e.submit(t, other)
			assert.NotEqual(t, http.StatusOK, code, got)
			assert.NotEqual(t, id, got["id"])
		})
	}
}

func TestXReviewClientRefOtherClientIsNewJob(t *testing.T) {
	e := newEnv(t)
	first := txt(map[string]any{"prompt": "p"})
	first["client_ref"] = "ref-1"
	st, _, _ := e.run(t, first)
	require.Equal(t, statusCompleted, st["status"], st)

	other := txt(map[string]any{"prompt": "p"})
	other["client_ref"] = "ref-1"
	other["client"] = "someone-else"
	code, got := e.submit(t, other)
	require.Equal(t, http.StatusAccepted, code, got)
	assert.NotEqual(t, st["id"], got["id"])
	assert.Len(t, xrJobs(t, e), 2)
}

func TestXReviewEmptyCheckpointLoadedNothingQueued(t *testing.T) {
	e := newEnv(t)
	l := newXrLeaser()
	e.f.SetLeaser(l.lease)
	st, _, _ := e.run(t, jobBody("txt2img", model("old_sd15.ckpt"), map[string]any{"prompt": "a"}))
	require.Equal(t, statusCompleted, st["status"], st)

	st, _, info := e.run(t, jobBody("txt2img", model(""), map[string]any{"prompt": "b"}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.Equal(t, "old_sd15.ckpt", info["checkpoint"])
	reqs := l.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "old_sd15.ckpt", reqs[1].Checkpoint)
}

func TestXReviewEmptyCheckpointFollowsQueuedJob(t *testing.T) {
	e := newEnv(t)
	l := newXrLeaser()
	e.f.SetLeaser(l.lease)
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "loaded"}))
	require.Equal(t, statusCompleted, st["status"], st)

	gate := l.blockCall(2)
	code, x := e.submit(t, jobBody("txt2img", model("old_sd15.ckpt"), map[string]any{"prompt": "x"}))
	require.Equal(t, http.StatusAccepted, code, x)
	l.waitCount(t, 2)

	code, empty := e.submit(t, jobBody("txt2img", model(""), map[string]any{"prompt": "empty"}))
	require.Equal(t, http.StatusAccepted, code, empty)
	close(gate)

	assert.Equal(t, statusCompleted, e.wait(t, x["id"].(string))["status"])
	final := e.wait(t, empty["id"].(string))
	require.Equal(t, statusCompleted, final["status"], final)

	reqs := l.requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, "old_sd15.ckpt", reqs[1].Checkpoint)
	assert.Equal(t, "old_sd15.ckpt", reqs[2].Checkpoint)
	res := decodeMap(t, e.do(t, http.MethodGet, "/jobs/"+empty["id"].(string)+"/result", nil))
	assert.Equal(t, "old_sd15.ckpt", sub(t, res, "info")["checkpoint"])
}

func TestXReviewEmptyCheckpointNothingLoadedNothingQueued(t *testing.T) {
	e := newEnv(t)
	l := newXrLeaser()
	e.f.SetLeaser(l.lease)
	code, st := e.submit(t, jobBody("txt2img", model(""), map[string]any{"prompt": "p"}))
	assert.Equal(t, http.StatusConflict, code, st)
	assert.Equal(t, "model_not_found", xrErr(t, st)["code"])
	assert.Empty(t, xrJobs(t, e))
	assert.Empty(t, l.requests())
	assert.Empty(t, e.rs.Calls())
}

func TestXReviewImg2ImgInvalidImagesRejectedAtSubmit(t *testing.T) {
	notImage := base64.StdEncoding.EncodeToString([]byte("definitely not an image"))
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"init_image not base64", map[string]any{"prompt": "p", "init_image": "!"}},
		{"init_image not an image", map[string]any{"prompt": "p", "init_image": notImage}},
		{"mask not base64", map[string]any{"prompt": "p", "init_image": pngB64(t, 8, 8), "mask": "!"}},
		{"mask not an image", map[string]any{"prompt": "p", "init_image": pngB64(t, 8, 8), "mask": notImage}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			l := newXrLeaser()
			e.f.SetLeaser(l.lease)
			code, st := e.submit(t, jobBody("img2img", model("sdxl_base.safetensors"), tc.params))
			assert.Equal(t, http.StatusBadRequest, code, st)
			assert.Equal(t, "invalid_params", xrErr(t, st)["code"])
			assert.Empty(t, xrJobs(t, e))
			assert.Empty(t, l.requests())
			assert.Empty(t, e.rs.Calls())
			assert.Empty(t, e.sd.imgGenBodies())
		})
	}
}

func TestXReviewResultInfoSizeFromImage(t *testing.T) {
	e := newEnv(t)
	st, imgs, info := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st["status"], st)
	require.NotEmpty(t, imgs)
	w, h := imageSize(t, imgs[0])
	require.NotZero(t, w)
	assert.EqualValues(t, w, info["width"])
	assert.EqualValues(t, h, info["height"])
}
