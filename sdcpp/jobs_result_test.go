package sdcpp

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResultServedFromFile(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		wantCode   int
		wantImages []any
		wantInfo   map[string]any
	}{
		{name: "written by finish", wantCode: http.StatusOK},
		{name: "old file with null images", file: `{"images":null,"info":{"seed":1}}`, wantCode: http.StatusOK, wantImages: []any{}, wantInfo: map[string]any{"seed": float64(1)}},
		{name: "old file with null info", file: `{"images":["a"],"info":null}`, wantCode: http.StatusOK, wantImages: []any{"a"}, wantInfo: map[string]any{}},
		{name: "normalized file streams as is", file: `{"images":["b"],"info":{"k":"v"}}`, wantCode: http.StatusOK, wantImages: []any{"b"}, wantInfo: map[string]any{"k": "v"}},
		{name: "broken file", file: `{"images":`, wantCode: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "sdjobs")
			e := newEnv(t, func(c *Config) { c.JobsDir = dir })
			code, st := e.submit(t, txt(map[string]any{"prompt": "p", "seed": 5}))
			require.Equal(t, http.StatusAccepted, code, st)
			id := st["id"].(string)
			require.Equal(t, statusCompleted, e.wait(t, id)["status"])
			resultPath := filepath.Join(dir, id, "result.json")
			if tc.file != "" {
				require.NoError(t, os.WriteFile(resultPath, []byte(tc.file), 0o600))
			}

			rec := e.do(t, http.MethodGet, "/jobs/"+id+"/result", nil)
			require.Equal(t, tc.wantCode, rec.Code, rec.Body.String())
			if tc.wantCode != http.StatusOK {
				return
			}
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			m := decodeMap(t, rec)
			if tc.file == "" {
				assert.Len(t, m["images"], 1)
				assert.Equal(t, float64(5), sub(t, m, "info")["seed"])
				return
			}
			assert.Equal(t, tc.wantImages, m["images"])
			assert.Equal(t, tc.wantInfo, m["info"])
		})
	}
}

func TestFinishWritesNormalizedResult(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sdjobs")
	e := newEnv(t, func(c *Config) { c.JobsDir = dir })
	j := &job{st: JobStatus{ID: "00112233445566778899aabb", Status: statusRunning}, cancel: func() {}}
	e.f.jobs.mu.Lock()
	e.f.jobs.jobs[j.st.ID] = j
	e.f.jobs.mu.Unlock()
	e.f.finish(j, statusCompleted, nil, &jobResult{})
	b, err := os.ReadFile(filepath.Join(dir, j.st.ID, "result.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"images":[],"info":{}}`, string(b))
}
