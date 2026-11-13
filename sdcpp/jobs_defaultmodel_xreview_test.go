package sdcpp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultModelBadVAEOrModuleRejectedAtSubmit(t *testing.T) {
	cases := []struct {
		name  string
		apply func(m map[string]any)
	}{
		{"missing vae", func(m map[string]any) { m["vae"] = "missing_vae.safetensors" }},
		{"missing module", func(m map[string]any) { m["modules"] = []string{"missing_module.safetensors"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			st, _, _ := e.run(t, txt(map[string]any{"prompt": "loaded"}))
			require.Equal(t, statusCompleted, st["status"], st)
			restarts := len(e.rs.Calls())
			gens := len(e.sd.imgGenBodies())
			jobs := len(xrJobs(t, e))

			empty := model("")
			tc.apply(empty)
			code, body := e.submit(t, jobBody("txt2img", empty, map[string]any{"prompt": "empty"}))
			assert.Equal(t, http.StatusBadRequest, code, body)
			var emptyErr any
			if code == http.StatusBadRequest {
				emptyErr = xrErr(t, body)["code"]
				assert.Contains(t, []any{"invalid_params", "model_not_found"}, emptyErr)
			}
			assert.Len(t, xrJobs(t, e), jobs)
			assert.Len(t, e.rs.Calls(), restarts)
			assert.Len(t, e.sd.imgGenBodies(), gens)

			explicit := model("sdxl_base.safetensors")
			tc.apply(explicit)
			expCode, expBody := e.submit(t, jobBody("txt2img", explicit, map[string]any{"prompt": "explicit"}))
			assert.Equal(t, code, expCode, expBody)
			assert.Equal(t, http.StatusBadRequest, expCode, expBody)
			if code == http.StatusBadRequest && expCode == http.StatusBadRequest {
				assert.Equal(t, emptyErr, xrErr(t, expBody)["code"], expBody)
			}
			assert.Len(t, xrJobs(t, e), jobs)
			assert.Len(t, e.rs.Calls(), restarts)
			assert.Len(t, e.sd.imgGenBodies(), gens)
		})
	}
}

func TestDefaultModelFollowsQueuedJobCancelledBeforeStart(t *testing.T) {
	e := newEnv(t)
	l := newXrLeaser()
	e.f.SetLeaser(l.lease)
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "loaded"}))
	require.Equal(t, statusCompleted, st["status"], st)

	gate := l.blockCall(2)
	code, x := e.submit(t, jobBody("txt2img", model("old_sd15.ckpt"), map[string]any{"prompt": "x"}))
	require.Equal(t, http.StatusAccepted, code, x)
	xID := x["id"].(string)
	l.waitCount(t, 2)

	code, b := e.submit(t, jobBody("txt2img", model(""), map[string]any{"prompt": "b"}))
	require.Equal(t, http.StatusAccepted, code, b)
	bID := b["id"].(string)

	rec := e.do(t, http.MethodPost, "/jobs/"+xID+"/cancel", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	close(gate)

	assert.Equal(t, statusCancelled, e.wait(t, xID)["status"])
	final := e.wait(t, bID)
	require.Equal(t, statusCompleted, final["status"], final)

	res := decodeMap(t, e.do(t, http.MethodGet, "/jobs/"+bID+"/result", nil))
	assert.Equal(t, "old_sd15.ckpt", sub(t, res, "info")["checkpoint"])
	reqs := l.requests()
	require.GreaterOrEqual(t, len(reqs), 3)
	assert.Equal(t, "old_sd15.ckpt", reqs[len(reqs)-1].Checkpoint)
}
