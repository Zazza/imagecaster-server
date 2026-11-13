package gpuqueue

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExclusiveSDLeasesDoNotOverlap(t *testing.T) {
	q := newTestQueue(10000, time.Second, time.Minute)
	first, err := q.Acquire(context.Background(), Spec{Kind: KindSD, Client: "a", WeightMB: 2000, NoWaitLimit: true, Exclusive: true})
	require.NoError(t, err)

	second, admitted, pos := q.TryAcquire(Spec{Kind: KindSD, Client: "b", WeightMB: 2000, NoWaitLimit: true, Exclusive: true})
	require.NotNil(t, second)
	assert.False(t, admitted)
	assert.Equal(t, 1, pos)
	st := q.Status()
	require.Len(t, st.Queue, 1)
	assert.Equal(t, "b", st.Queue[0].Client)

	llm, ok, _ := q.TryAcquire(Spec{Kind: KindLLM, Client: "llm", WeightMB: 2000})
	assert.True(t, ok, "LLM is not blocked by a waiting SD job")
	llm.Release()

	plain, ok, _ := q.TryAcquire(Spec{Kind: KindSD, Client: "ext", WeightMB: 1000})
	assert.True(t, ok, "non-exclusive SD lease is not affected")
	plain.Release()

	first.Release()
	assert.True(t, waitFor(t, time.Second, func() bool { return len(q.Status().Running) == 1 && len(q.Status().Queue) == 0 }))
	assert.Equal(t, "b", q.Status().Running[0].Client)
	second.Release()
}
