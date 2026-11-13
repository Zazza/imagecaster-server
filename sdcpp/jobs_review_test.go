package sdcpp

import (
	"context"
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

func TestEmptyCheckpointUsesLoadedModel(t *testing.T) {
	e := newEnv(t)
	touch(t, filepath.Join(e.cfg.VAEDir, "sdxl_vae.safetensors"))
	m := model("sdxl_base.safetensors")
	m["vae"] = "sdxl_vae.safetensors"
	st, _, _ := e.run(t, jobBody("txt2img", m, map[string]any{"prompt": "first"}))
	require.Equal(t, statusCompleted, st["status"], st)

	st, _, info := e.run(t, jobBody("txt2img", model(""), map[string]any{"prompt": "second"}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.Len(t, e.rs.Calls(), 1)
	assert.Equal(t, "sdxl_base.safetensors", info["checkpoint"])
	assert.Equal(t, "sdxl_vae.safetensors", info["vae"])
}

func TestEmptyCheckpointOwnVAEReloads(t *testing.T) {
	e := newEnv(t)
	touch(t, filepath.Join(e.cfg.VAEDir, "other_vae.safetensors"))
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "first"}))
	require.Equal(t, statusCompleted, st["status"], st)

	m := model("")
	m["vae"] = "other_vae.safetensors"
	st, _, info := e.run(t, jobBody("txt2img", m, map[string]any{"prompt": "second"}))
	require.Equal(t, statusCompleted, st["status"], st)
	require.Len(t, e.rs.Calls(), 2)
	assert.Equal(t, "sdxl_base.safetensors", info["checkpoint"])
	assert.Equal(t, "other_vae.safetensors", info["vae"])
}

func TestEmptyCheckpointNothingLoadedFails(t *testing.T) {
	e := newEnv(t)
	code, st := e.submit(t, jobBody("txt2img", model(""), map[string]any{"prompt": "p"}))
	assert.Equal(t, http.StatusConflict, code)
	jerr := sub(t, st, "error")
	assert.Equal(t, "model_not_found", jerr["code"])
	assert.Contains(t, jerr["message"], "no model is loaded")
	assert.Empty(t, e.rs.Calls())
}

func TestEmptyCheckpointLeaseUsesLoadedModel(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var got []string
	e.f.SetLeaser(func(ctx context.Context, req LeaseRequest) (func(), error) {
		mu.Lock()
		got = append(got, req.Checkpoint)
		mu.Unlock()
		return func() {}, nil
	})
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "a"}))
	require.Equal(t, statusCompleted, st["status"], st)
	st, _, _ = e.run(t, jobBody("txt2img", model(""), map[string]any{"prompt": "b"}))
	require.Equal(t, statusCompleted, st["status"], st)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"sdxl_base.safetensors", "sdxl_base.safetensors"}, got)
}

func TestResultInfoCarriesParams(t *testing.T) {
	e := newEnv(t)
	st, _, info := e.run(t, txt(map[string]any{
		"prompt": "cat", "negative_prompt": "dog", "sampler": "euler_a", "scheduler": "karras",
		"steps": 12, "cfg_scale": 5.5, "seed": 42, "width": 512, "height": 768,
	}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.Equal(t, "sdxl_base.safetensors", info["checkpoint"])
	assert.Equal(t, "euler_a", info["sampler"])
	assert.Equal(t, "karras", info["scheduler"])
	assert.EqualValues(t, 12, info["steps"])
	assert.EqualValues(t, 5.5, info["cfg_scale"])
	assert.EqualValues(t, 42, info["seed"])
	assert.EqualValues(t, 512, info["width"])
	assert.EqualValues(t, 768, info["height"])
	assert.Equal(t, "cat", info["prompt"])
	assert.Equal(t, "dog", info["negative_prompt"])
	assert.Contains(t, info, "vae")
	_, has := info["denoising_strength"]
	assert.False(t, has)

	st, _, info = e.run(t, txt(map[string]any{"prompt": "x"}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.EqualValues(t, 20, info["steps"])
	assert.EqualValues(t, 7, info["cfg_scale"])
	seed, ok := info["seed"].(float64)
	require.True(t, ok)
	assert.GreaterOrEqual(t, seed, 0.0)

	st, _, info = e.run(t, jobBody("img2img", model("sdxl_base.safetensors"), map[string]any{"prompt": "e", "init_image": pngB64(t, 8, 8), "denoising_strength": 0.4}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.EqualValues(t, 0.4, info["denoising_strength"])
}

func TestFinishedJobDropsParams(t *testing.T) {
	e := newEnv(t)
	st, _, _ := e.run(t, jobBody("img2img", model("sdxl_base.safetensors"), map[string]any{"prompt": "e", "init_image": pngB64(t, 8, 8)}))
	require.Equal(t, statusCompleted, st["status"], st)
	e.f.jobs.mu.Lock()
	j := e.f.jobs.jobs[st["id"].(string)]
	params := j.params
	e.f.jobs.mu.Unlock()
	assert.Equal(t, GenParams{}, params)
}

func TestJobFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "sdjobs")
	e := newEnv(t, func(c *Config) { c.JobsDir = dir })
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st["status"], st)
	jd := filepath.Join(dir, st["id"].(string))
	for p, want := range map[string]os.FileMode{
		dir:                              0o700,
		jd:                               0o700,
		filepath.Join(jd, "job.json"):    0o600,
		filepath.Join(jd, "result.json"): 0o600,
	} {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, want, fi.Mode().Perm(), p)
	}
}

func TestOldJobFilesTightenedOnRestore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "sdjobs")
	e := newEnv(t, func(c *Config) { c.JobsDir = dir })
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st["status"], st)
	jd := filepath.Join(dir, st["id"].(string))
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, os.Chmod(jd, 0o755))
	require.NoError(t, os.Chmod(filepath.Join(jd, "result.json"), 0o644))

	newJobStore(dir, time.Hour, time.Now)
	for p, want := range map[string]os.FileMode{dir: 0o700, jd: 0o700, filepath.Join(jd, "result.json"): 0o600} {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, want, fi.Mode().Perm(), p)
	}
}

func TestCancelQueuedJobIsSynchronous(t *testing.T) {
	e := newEnv(t)
	e.f.SetLeaser(func(ctx context.Context, req LeaseRequest) (func(), error) {
		<-ctx.Done()
		return nil, context.Canceled
	})
	code, st := e.submit(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, http.StatusAccepted, code, st)
	id := st["id"].(string)
	rec := e.do(t, http.MethodPost, "/jobs/"+id+"/cancel", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeMap(t, rec)
	assert.Equal(t, statusCancelled, body["status"])
	assert.Equal(t, "cancelled", sub(t, body, "error")["code"])
	final := e.wait(t, id)
	assert.Equal(t, statusCancelled, final["status"])
	assert.Empty(t, e.sd.imgGenBodies())
}
