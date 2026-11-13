package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
	"imagecaster-server/gpuqueue"
	"imagecaster-server/models"
	"imagecaster-server/sdcpp"
)

func newXReviewLimitEnv(t *testing.T, maxLen int) *wireEnv {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "m.safetensors"), make([]byte, 3*1024*1024), 0o644))
	q := gpuqueue.New(gpuqueue.Config{TotalBudgetMB: 5000, MaxWait: time.Second, LeaseTTL: 30 * time.Second, MaxQueueLen: maxLen})
	cfg := GateConfig{SDDefaultWeightMB: 9000, SDOverheadMB: 100, LLMWeightMB: 2000, MaxWaitSeconds: 1}
	gate := NewGate(q, cfg, newSDWeight(cfg, models.NewModelManager(&config.Config{}), dir))
	sd, srv := newWireSD(t)
	f := sdcpp.New(sdcpp.Config{BaseURL: srv.URL, ModelsDir: dir, PollInterval: 5 * time.Millisecond, ReadyTimeout: time.Second}, noopRestarter{}, nil)
	wireSDJobs(f, gate)
	return &wireEnv{f: f, q: q, sd: sd, gate: gate}
}

func fillGPUQueueWithLLM(t *testing.T, q *gpuqueue.Queue, n int) {
	blocker, err := q.Acquire(t.Context(), gpuqueue.Spec{Kind: gpuqueue.KindLLM, Client: "llm", WeightMB: 5000})
	require.NoError(t, err)
	t.Cleanup(blocker.Release)
	for i := 0; i < n; i++ {
		l, acquired, pos := q.TryAcquire(gpuqueue.Spec{Kind: gpuqueue.KindLLM, Client: "llm", WeightMB: 2000})
		require.False(t, acquired)
		require.NotNil(t, l)
		require.Positive(t, pos)
		t.Cleanup(l.Release)
	}
	require.True(t, q.QueueFull())
}

func TestXReviewSDJobNotQueueFullWhenGPUQueueFullOfLLM(t *testing.T) {
	e := newXReviewLimitEnv(t, 2)
	fillGPUQueueWithLLM(t, e.q, 2)

	id := e.submit(t)
	require.True(t, waitFor(t, 2*time.Second, func() bool { return len(e.q.Status().Queue) == 3 }))
	time.Sleep(1100 * time.Millisecond)
	js := e.status(t, id)
	assert.Equal(t, "queued", js["status"], js)
	assert.Nil(t, js["error"], js)
	var sdWaiting int
	for _, j := range e.q.Status().Queue {
		if j.Kind == gpuqueue.KindSD {
			sdWaiting++
		}
	}
	assert.Equal(t, 1, sdWaiting)

	code, _ := e.do(t, http.MethodPost, "/jobs/"+id+"/cancel", "")
	require.Equal(t, http.StatusOK, code)
	require.True(t, waitFor(t, 2*time.Second, func() bool { return e.status(t, id)["status"] == "cancelled" }))
}

func TestXReviewLeaseSDEnqueuesWhenGPUQueueFull(t *testing.T) {
	e := newXReviewLimitEnv(t, 2)
	fillGPUQueueWithLLM(t, e.q, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		rel, err := e.gate.LeaseSD(ctx, sdcpp.LeaseRequest{JobID: "jx", Client: "imagecaster"})
		if rel != nil {
			rel()
		}
		errc <- err
	}()
	require.True(t, waitFor(t, time.Second, func() bool { return len(e.q.Status().Queue) == 3 }))
	select {
	case err := <-errc:
		t.Fatalf("LeaseSD returned instead of waiting: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	assert.Equal(t, 3, e.gate.SDPosition("jx"))
	cancel()
	err := <-errc
	assert.NotErrorIs(t, err, sdcpp.ErrQueueFull)
}
