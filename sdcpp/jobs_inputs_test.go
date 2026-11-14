package sdcpp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gatedLeaser() (func(ctx context.Context, req LeaseRequest) (func(), error), chan struct{}) {
	open := make(chan struct{})
	return func(ctx context.Context, req LeaseRequest) (func(), error) {
		select {
		case <-open:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, open
}

func (e *testEnv) queuedParams(t *testing.T, id string) GenParams {
	t.Helper()
	e.f.jobs.mu.Lock()
	defer e.f.jobs.mu.Unlock()
	j := e.f.jobs.jobs[id]
	require.NotNil(t, j)
	return j.params
}

func TestQueuedJobImagesLiveOnDisk(t *testing.T) {
	initImg, mask, src := pngB64(t, 8, 8), pngB64(t, 8, 8), pngB64(t, 4, 4)
	cases := []struct {
		name     string
		jobsDir  bool
		body     map[string]any
		fields   []string
		inMemory func(p GenParams) []string
	}{
		{
			name:     "img2img with mask",
			jobsDir:  true,
			body:     jobBody("img2img", model("sdxl_base.safetensors"), map[string]any{"prompt": "e", "init_image": initImg, "mask": mask}),
			fields:   []string{"init_image", "mask"},
			inMemory: func(p GenParams) []string { return []string{p.InitImage, p.Mask} },
		},
		{
			name:     "upscale",
			jobsDir:  true,
			body:     jobBody("upscale", nil, map[string]any{"image": src, "upscaler": "lanczos", "scale": 2}),
			fields:   []string{"image"},
			inMemory: func(p GenParams) []string { return []string{p.Image} },
		},
		{
			name:     "without jobs dir images stay in memory",
			body:     jobBody("img2img", model("sdxl_base.safetensors"), map[string]any{"prompt": "e", "init_image": initImg}),
			inMemory: func(p GenParams) []string { return []string{p.InitImage} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := ""
			if tc.jobsDir {
				dir = filepath.Join(t.TempDir(), "sdjobs")
			}
			e := newEnv(t, func(c *Config) { c.JobsDir = dir })
			leaser, open := gatedLeaser()
			e.f.SetLeaser(leaser)

			code, st := e.submit(t, tc.body)
			require.Equal(t, http.StatusAccepted, code, st)
			id := st["id"].(string)

			mem := tc.inMemory(e.queuedParams(t, id))
			for _, v := range mem {
				if tc.jobsDir {
					assert.Empty(t, v, "queued job must not keep the image in memory")
				} else {
					assert.NotEmpty(t, v)
				}
			}
			for _, f := range tc.fields {
				assert.FileExists(t, filepath.Join(dir, id, inputPrefix+f))
			}

			close(open)
			st = e.wait(t, id)
			require.Equal(t, statusCompleted, st["status"], st)
			for _, f := range tc.fields {
				assert.NoFileExists(t, filepath.Join(dir, id, inputPrefix+f))
			}
			if tc.body["kind"] == "img2img" {
				bodies := e.sd.imgGenBodies()
				require.Len(t, bodies, 1)
				assert.Equal(t, initImg, bodies[0]["init_image"])
				if _, ok := tc.body["params"].(map[string]any)["mask"]; ok {
					assert.Equal(t, mask, bodies[0]["mask_image"])
				}
			}
		})
	}
}

func TestRejectedSubmitLeavesNoInputFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sdjobs")
	e := newEnv(t, func(c *Config) { c.JobsDir = dir })
	leaser, open := gatedLeaser()
	e.f.SetLeaser(leaser)
	t.Cleanup(func() { close(open) })
	body := jobBody("img2img", model("sdxl_base.safetensors"), map[string]any{"prompt": "e", "init_image": pngB64(t, 8, 8)})
	body["client_ref"] = "same"
	code, st := e.submit(t, body)
	require.Equal(t, http.StatusAccepted, code, st)
	code, st2 := e.submit(t, body)
	require.Equal(t, http.StatusOK, code, st2)
	assert.Equal(t, st["id"], st2["id"])
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, ents, 1)
}

func TestRestoreDropsInputsOfLostJobs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sdjobs")
	id := "0123456789abcdef0123"
	jd := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(jd, 0o700))
	now := time.Now().UTC()
	b, err := json.Marshal(JobStatus{ID: id, Kind: kindImg2Img, Status: statusQueued, CreatedAt: &now})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(jd, "job.json"), b, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(jd, inputPrefix+"init_image"), []byte("x"), 0o600))

	s := newJobStore(dir, time.Hour, time.Now)
	require.Contains(t, s.jobs, id)
	assert.Equal(t, statusLost, s.jobs[id].st.Status)
	assert.NoFileExists(t, filepath.Join(jd, inputPrefix+"init_image"))
}

func TestInputFilesRemovedWhenJobEndsWithoutResult(t *testing.T) {
	cases := []struct {
		name       string
		wantStatus string
		setup      func(e *testEnv) func(id string)
	}{
		{
			name:       "cancelled while queued",
			wantStatus: statusCancelled,
			setup: func(e *testEnv) func(string) {
				leaser, _ := gatedLeaser()
				e.f.SetLeaser(leaser)
				return func(id string) {
					require.Equal(t, http.StatusOK, e.do(t, http.MethodPost, "/jobs/"+id+"/cancel", nil).Code)
				}
			},
		},
		{
			name:       "failed",
			wantStatus: statusFailed,
			setup: func(e *testEnv) func(string) {
				e.sd.set(func(f *fakeSD) { f.crash = true })
				return func(string) {}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "sdjobs")
			e := newEnv(t, func(c *Config) { c.JobsDir = dir })
			act := tc.setup(e)
			code, st := e.submit(t, jobBody("img2img", model("sdxl_base.safetensors"), map[string]any{"prompt": "e", "init_image": pngB64(t, 8, 8), "mask": pngB64(t, 8, 8)}))
			require.Equal(t, http.StatusAccepted, code, st)
			id := st["id"].(string)
			act(id)
			assert.Equal(t, tc.wantStatus, e.wait(t, id)["status"])
			for _, f := range []string{"init_image", "mask"} {
				assert.NoFileExists(t, filepath.Join(dir, id, inputPrefix+f))
			}
		})
	}
}
