package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"imagecaster-server/sdcpp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaseTrainTakesWholeBudgetAndBlocksSD(t *testing.T) {
	gate, q := newTestGate(16000, 1, GateConfig{SDDefaultWeightMB: 3000, LLMWeightMB: 2000}, "")
	var evicted atomic.Int32
	gate.SetBeforeSD(func(ctx context.Context, w int) { evicted.Add(1) })

	release, err := gate.LeaseTrain(context.Background(), "lora-train", nil)
	require.NoError(t, err)
	st := q.Status()
	require.Len(t, st.Running, 1)
	assert.Equal(t, 16000, st.Running[0].WeightMB)
	assert.Equal(t, int32(1), evicted.Load())

	got := make(chan func(), 1)
	go func() {
		rel, err := gate.LeaseSD(context.Background(), sdcpp.LeaseRequest{JobID: "j1", Client: "app"})
		if err == nil {
			got <- rel
		}
	}()
	require.True(t, waitFor(t, time.Second, func() bool { return len(q.Status().Queue) == 1 }))
	select {
	case <-got:
		t.Fatal("SD job got the GPU while training holds it")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case rel := <-got:
		rel()
	case <-time.After(2 * time.Second):
		t.Fatal("SD job did not start after training released the GPU")
	}
}

func TestLeaseTrainLostCallsOnLost(t *testing.T) {
	gate, q := newTestGate(16000, 1, GateConfig{SDDefaultWeightMB: 3000}, "")
	lost := make(chan struct{}, 1)
	release, err := gate.LeaseTrain(context.Background(), "lora-train", func() { lost <- struct{}{} })
	require.NoError(t, err)
	defer release()

	st := q.Status()
	require.Len(t, st.Running, 1)
	require.True(t, q.Cancel(st.Running[0].ID))
	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("onLost was not called after the lease was cancelled")
	}
}

func TestLeaseTrainReleaseDoesNotCallOnLost(t *testing.T) {
	gate, _ := newTestGate(16000, 1, GateConfig{SDDefaultWeightMB: 3000}, "")
	var calls atomic.Int32
	release, err := gate.LeaseTrain(context.Background(), "lora-train", func() { calls.Add(1) })
	require.NoError(t, err)
	release()
	release()
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, calls.Load())
}
