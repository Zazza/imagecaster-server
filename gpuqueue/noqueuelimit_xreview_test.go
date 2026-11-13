package gpuqueue

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLimitQueue(t *testing.T, maxLen int) *Queue {
	t.Helper()
	q := New(Config{TotalBudgetMB: 1000, MaxWait: time.Hour, LeaseTTL: time.Hour, MaxWarnings: 20, MaxQueueLen: maxLen})
	holder, err := q.Acquire(context.Background(), Spec{Kind: KindLLM, Client: "holder", WeightMB: 1000})
	require.NoError(t, err)
	t.Cleanup(holder.Release)
	return q
}

func fillWaiting(t *testing.T, q *Queue, n int, noLimit bool) []*Lease {
	t.Helper()
	out := make([]*Lease, 0, n)
	for i := 0; i < n; i++ {
		l, acquired, pos := q.TryAcquire(Spec{Kind: KindLLM, Client: "w", WeightMB: 1000, NoQueueLimit: noLimit})
		require.False(t, acquired)
		require.NotNil(t, l)
		require.Positive(t, pos)
		out = append(out, l)
	}
	return out
}

func TestXReviewNoQueueLimit_TryAcquireAcceptedWhenQueueFull(t *testing.T) {
	q := newLimitQueue(t, 2)
	fillWaiting(t, q, 2, false)
	require.True(t, q.QueueFull())

	l, acquired, pos := q.TryAcquire(Spec{Kind: KindSD, Client: "sd", WeightMB: 1000, NoQueueLimit: true})
	require.NotNil(t, l)
	assert.False(t, acquired)
	assert.Equal(t, 3, pos)
	assert.Len(t, q.Status().Queue, 3)
}

func TestXReviewNoQueueLimit_AcquireNoErrQueueFullWhenQueueFull(t *testing.T) {
	q := newLimitQueue(t, 2)
	fillWaiting(t, q, 2, false)
	require.True(t, q.QueueFull())

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		l, err := q.Acquire(ctx, Spec{Kind: KindSD, Client: "sd", WeightMB: 1000, NoQueueLimit: true})
		if l != nil {
			l.Release()
		}
		errc <- err
	}()
	require.True(t, waitFor(t, time.Second, func() bool { return len(q.Status().Queue) == 3 }))
	err := <-errc
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrQueueFull)
}

func TestXReviewNoQueueLimit_ZeroLimitWaitersStillAcceptNormal(t *testing.T) {
	q := newLimitQueue(t, 2)
	fillWaiting(t, q, 2, true)
	assert.False(t, q.QueueFull())

	l, acquired, pos := q.TryAcquire(Spec{Kind: KindLLM, Client: "n", WeightMB: 1000})
	require.NotNil(t, l)
	assert.False(t, acquired)
	assert.Equal(t, 3, pos)
	assert.False(t, q.QueueFull())

	l2, _, pos2 := q.TryAcquire(Spec{Kind: KindLLM, Client: "n", WeightMB: 1000})
	require.NotNil(t, l2)
	assert.Equal(t, 4, pos2)
	assert.True(t, q.QueueFull())

	l3, acquired3, pos3 := q.TryAcquire(Spec{Kind: KindLLM, Client: "n", WeightMB: 1000})
	assert.Nil(t, l3)
	assert.False(t, acquired3)
	assert.Zero(t, pos3)
}

func TestXReviewNoQueueLimit_NormalWaitersFillQueue(t *testing.T) {
	q := newLimitQueue(t, 2)
	fillWaiting(t, q, 2, false)
	assert.True(t, q.QueueFull())

	_, err := q.Acquire(context.Background(), Spec{Kind: KindLLM, Client: "n", WeightMB: 1000})
	require.ErrorIs(t, err, ErrQueueFull)
	l, acquired, pos := q.TryAcquire(Spec{Kind: KindLLM, Client: "n", WeightMB: 1000})
	assert.Nil(t, l)
	assert.False(t, acquired)
	assert.Zero(t, pos)
	assert.Len(t, q.Status().Queue, 2)

	nl, _, npos := q.TryAcquire(Spec{Kind: KindSD, Client: "sd", WeightMB: 1000, NoQueueLimit: true})
	require.NotNil(t, nl)
	assert.Equal(t, 3, npos)
	assert.True(t, q.QueueFull())
}

func TestXReviewNoQueueLimit_MaxQueueLenOne(t *testing.T) {
	q := newLimitQueue(t, 1)
	fillWaiting(t, q, 1, false)
	require.True(t, q.QueueFull())
	for i := 0; i < 5; i++ {
		l, _, _ := q.TryAcquire(Spec{Kind: KindSD, Client: "sd", WeightMB: 1000, NoQueueLimit: true})
		require.NotNil(t, l)
	}
	assert.Len(t, q.Status().Queue, 6)
}

func TestXReviewNoQueueLimit_FreedSlotAfterNormalCancel(t *testing.T) {
	q := newLimitQueue(t, 2)
	fillWaiting(t, q, 1, true)
	normal := fillWaiting(t, q, 2, false)
	require.True(t, q.QueueFull())
	require.True(t, q.Cancel(normal[0].ID()))
	assert.False(t, q.QueueFull())
	l, _, _ := q.TryAcquire(Spec{Kind: KindLLM, Client: "n", WeightMB: 1000})
	assert.NotNil(t, l)
}

func acquireWaiting(t *testing.T, q *Queue, spec Spec) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan struct{}, 1)
	errc := make(chan error, 1)
	spec.NoWaitLimit = true
	spec.OnQueued = func(string) { queued <- struct{}{} }
	go func() {
		l, err := q.Acquire(ctx, spec)
		if l != nil {
			l.Release()
		}
		errc <- err
	}()
	t.Cleanup(cancel)
	select {
	case <-queued:
	case err := <-errc:
		errc <- err
	case <-time.After(time.Second):
		t.Fatal("acquire neither queued nor returned")
	}
	return errc
}

func requireStillWaiting(t *testing.T, errc <-chan error) {
	t.Helper()
	select {
	case err := <-errc:
		t.Fatalf("acquire returned instead of waiting: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestXReviewNoQueueLimit_AcquirePath_ZeroLimitWaitersDoNotCount(t *testing.T) {
	q := newLimitQueue(t, 2)
	for i := 0; i < 2; i++ {
		requireStillWaiting(t, acquireWaiting(t, q, Spec{Kind: KindSD, Client: "sd", WeightMB: 1000, NoQueueLimit: true}))
	}
	assert.False(t, q.QueueFull())
	requireStillWaiting(t, acquireWaiting(t, q, Spec{Kind: KindLLM, Client: "n", WeightMB: 1000}))
	assert.False(t, q.QueueFull())
	assert.Len(t, q.Status().Queue, 3)
}

func TestXReviewNoQueueLimit_AcquirePath_NormalWaitersFillQueue(t *testing.T) {
	q := newLimitQueue(t, 2)
	for i := 0; i < 2; i++ {
		requireStillWaiting(t, acquireWaiting(t, q, Spec{Kind: KindLLM, Client: "n", WeightMB: 1000}))
	}
	assert.True(t, q.QueueFull())
	_, err := q.Acquire(context.Background(), Spec{Kind: KindLLM, Client: "n", WeightMB: 1000})
	require.ErrorIs(t, err, ErrQueueFull)
	requireStillWaiting(t, acquireWaiting(t, q, Spec{Kind: KindSD, Client: "sd", WeightMB: 1000, NoQueueLimit: true}))
	assert.Len(t, q.Status().Queue, 3)
	assert.True(t, q.QueueFull())
}
