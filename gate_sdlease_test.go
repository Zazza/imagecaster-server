package main

import (
	"context"
	"testing"
	"time"

	"imagecaster-server/sdcpp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaseSDOneJobAtATime(t *testing.T) {
	gate, q := newTestGate(20000, 1, GateConfig{SDDefaultWeightMB: 3000, LLMWeightMB: 2000}, "")
	rel1, err := gate.LeaseSD(context.Background(), sdcpp.LeaseRequest{JobID: "j1", Client: "a"})
	require.NoError(t, err)

	got := make(chan func(), 1)
	go func() {
		rel2, err := gate.LeaseSD(context.Background(), sdcpp.LeaseRequest{JobID: "j2", Client: "b"})
		if err == nil {
			got <- rel2
		}
	}()
	require.True(t, waitFor(t, time.Second, func() bool { return len(q.Status().Queue) == 1 }))
	select {
	case <-got:
		t.Fatal("second SD job got a lease while the first holds one")
	case <-time.After(50 * time.Millisecond):
	}
	assert.Equal(t, 1, gate.SDPosition("j2"))

	rel1()
	select {
	case rel2 := <-got:
		rel2()
	case <-time.After(2 * time.Second):
		t.Fatal("second SD job did not get the lease after the first finished")
	}
}
