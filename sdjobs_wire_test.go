package main

import (
	"encoding/json"
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

	"imagecaster-server/gpuqueue"
	"imagecaster-server/sdcpp"
)

type wireSD struct {
	mu      sync.Mutex
	cancels []string
}

func newWireSD(t *testing.T) (*wireSD, *httptest.Server) {
	w := &wireSD{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sdcpp/v1/capabilities", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /sdcpp/v1/img_gen", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusAccepted)
		rw.Write([]byte(`{"id":"n1","status":"queued"}`))
	})
	mux.HandleFunc("GET /sdcpp/v1/jobs/{id}", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		cancelled := len(w.cancels) > 0
		w.mu.Unlock()
		if cancelled {
			rw.Write([]byte(`{"id":"n1","status":"cancelled"}`))
			return
		}
		rw.Write([]byte(`{"id":"n1","status":"generating","progress":{"completed":1,"total":4,"unit":"steps"}}`))
	})
	mux.HandleFunc("POST /sdcpp/v1/jobs/{id}/cancel", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.cancels = append(w.cancels, r.PathValue("id"))
		w.mu.Unlock()
		rw.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return w, srv
}

type noopRestarter struct{}

func (noopRestarter) RestartWithArgs([]string) error { return nil }

type wireEnv struct {
	f    *sdcpp.Facade
	q    *gpuqueue.Queue
	sd   *wireSD
	gate *Gate
}

func newWireEnv(t *testing.T, budget int) *wireEnv {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "m.safetensors"), make([]byte, 3*1024*1024), 0o644))
	cfg := GateConfig{SDDefaultWeightMB: 9000, SDOverheadMB: 100, LLMWeightMB: 2000, MaxWaitSeconds: 1}
	gate, q := newTestGate(budget, 1, cfg, dir)
	sd, srv := newWireSD(t)
	f := sdcpp.New(sdcpp.Config{BaseURL: srv.URL, ModelsDir: dir, PollInterval: 5 * time.Millisecond, ReadyTimeout: time.Second}, noopRestarter{}, nil)
	wireSDJobs(f, gate)
	return &wireEnv{f: f, q: q, sd: sd, gate: gate}
}

func (e *wireEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	rec := httptest.NewRecorder()
	e.f.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return rec.Code, m
}

func (e *wireEnv) submit(t *testing.T) string {
	code, m := e.do(t, http.MethodPost, "/jobs", `{"kind":"txt2img","client":"imagecaster","model":{"checkpoint":"m.safetensors"},"params":{"prompt":"a"}}`)
	require.Equal(t, http.StatusAccepted, code, m)
	return m["id"].(string)
}

func (e *wireEnv) status(t *testing.T, id string) map[string]any {
	_, m := e.do(t, http.MethodGet, "/jobs/"+id, "")
	return m
}

func TestSDJobWaitsVisiblyInGPUQueueAndCancelsFromThere(t *testing.T) {
	e := newWireEnv(t, 5000)
	blocker, err := e.q.Acquire(t.Context(), gpuqueue.Spec{Kind: gpuqueue.KindLLM, Client: "llm", WeightMB: 5000})
	require.NoError(t, err)
	defer blocker.Release()

	id := e.submit(t)
	require.True(t, waitFor(t, 2*time.Second, func() bool { return len(e.q.Status().Queue) == 1 }))
	time.Sleep(1100 * time.Millisecond)
	st := e.q.Status()
	require.Len(t, st.Queue, 1, "job must keep waiting past max_wait")
	assert.Equal(t, gpuqueue.KindSD, st.Queue[0].Kind)
	assert.Equal(t, "imagecaster", st.Queue[0].Client)
	assert.Equal(t, 3+100, st.Queue[0].WeightMB)
	js := e.status(t, id)
	assert.Equal(t, "queued", js["status"])
	assert.EqualValues(t, 1, js["position"])

	require.True(t, e.q.Cancel(st.Queue[0].ID))
	require.True(t, waitFor(t, 2*time.Second, func() bool { return e.status(t, id)["status"] == "cancelled" }))
	assert.Empty(t, e.q.Status().Queue)
}

func TestSDJobRunningLeaseCancelledFromGPUQueue(t *testing.T) {
	e := newWireEnv(t, 20000)
	id := e.submit(t)
	require.True(t, waitFor(t, 2*time.Second, func() bool { return e.status(t, id)["status"] == "running" }))
	st := e.q.Status()
	require.Len(t, st.Running, 1)
	require.True(t, e.q.Cancel(st.Running[0].ID))
	require.True(t, waitFor(t, 2*time.Second, func() bool { return e.status(t, id)["status"] == "cancelled" }))
	e.sd.mu.Lock()
	assert.Equal(t, []string{"n1"}, e.sd.cancels)
	e.sd.mu.Unlock()
	assert.Empty(t, e.q.Status().Running)
}

func TestSDJobLeaseReleasedAfterCancelByAPI(t *testing.T) {
	e := newWireEnv(t, 20000)
	id := e.submit(t)
	require.True(t, waitFor(t, 2*time.Second, func() bool { return e.status(t, id)["status"] == "running" }))
	code, _ := e.do(t, http.MethodPost, "/jobs/"+id+"/cancel", "")
	require.Equal(t, http.StatusOK, code)
	require.True(t, waitFor(t, 2*time.Second, func() bool { return e.status(t, id)["status"] == "cancelled" }))
	require.True(t, waitFor(t, time.Second, func() bool { return len(e.q.Status().Running) == 0 }))
}

func TestSDJobUnknownCancelNotFound(t *testing.T) {
	e := newWireEnv(t, 20000)
	code, m := e.do(t, http.MethodPost, "/jobs/0123456789abcdef/cancel", "")
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "not_found", m["error"].(map[string]any)["code"])
}

func TestSDJobQueueFull(t *testing.T) {
	e := newWireEnv(t, 5000)
	blocker, err := e.q.Acquire(t.Context(), gpuqueue.Spec{Kind: gpuqueue.KindLLM, Client: "llm", WeightMB: 5000})
	require.NoError(t, err)
	defer blocker.Release()
	var last int
	var body map[string]any
	for i := 0; i < 101; i++ {
		last, body = e.do(t, http.MethodPost, "/jobs", `{"kind":"txt2img","client":"imagecaster","model":{"checkpoint":"m.safetensors"},"params":{"prompt":"a"}}`)
		if last != http.StatusAccepted {
			break
		}
	}
	assert.Equal(t, http.StatusTooManyRequests, last)
	assert.Equal(t, "queue_full", body["error"].(map[string]any)["code"])
}
