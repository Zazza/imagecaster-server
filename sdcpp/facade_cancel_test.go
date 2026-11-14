package sdcpp

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancelDuringModelLoadReleasesGeneratorPromptly(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadyTimeout = time.Minute })
	e.rs.setHook(func() { e.sd.set(func(f *fakeSD) { f.notReady = 1 << 30 }) })

	code, st := e.submit(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, http.StatusAccepted, code, st)
	id := st["id"].(string)
	require.Eventually(t, func() bool {
		var gets int
		e.sd.set(func(f *fakeSD) { gets = f.capGets })
		return gets > 0
	}, 2*time.Second, 5*time.Millisecond)

	require.Equal(t, http.StatusOK, e.do(t, http.MethodPost, "/jobs/"+id+"/cancel", nil).Code)
	assert.Equal(t, statusCancelled, e.wait(t, id)["status"])
	require.Eventually(t, func() bool { return len(e.f.gen) == 0 }, time.Second, 5*time.Millisecond, "model load must stop on cancel and free the generator")
	var before, after int
	e.sd.set(func(f *fakeSD) { before = f.capGets })
	time.Sleep(50 * time.Millisecond)
	e.sd.set(func(f *fakeSD) { after = f.capGets })
	assert.Equal(t, before, after, "readiness polling must stop after cancel")

	e.sd.set(func(f *fakeSD) { f.notReady = 0 })
	e.rs.setHook(nil)
	start := time.Now()
	st2, imgs, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st2["status"], st2)
	assert.Len(t, imgs, 1)
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.Len(t, e.rs.Calls(), 1)
}
