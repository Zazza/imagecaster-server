package sdcpp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobsCacheModeReachesSDCpp(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		option   string
		wantMode string
		wantInfo bool
	}{
		{"no cache by default", "", "", "", false},
		{"spectrum", "spectrum", "", "spectrum", true},
		{"easycache with options", "easycache", "threshold=0.2,start=0.15", "easycache", true},
		{"spectrum with signed and short numbers", "spectrum", "w=-.4,m=3,stop=0.9", "spectrum", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newJcEnv(t)
			e.f.SetLeaser(nil)
			body := jcTxt("test", "", "sdxl_base.safetensors", "a cat")
			params := body["params"].(map[string]any)
			if tc.mode != "" {
				params["cache_mode"] = tc.mode
			}
			if tc.option != "" {
				params["cache_option"] = tc.option
			}
			id := jcID(jcSubmit(t, e.f, body, http.StatusAccepted))
			jcWaitStatus(t, e.f, id, "completed")
			_, info := jcResult(t, e.f, id)

			e.sd.mu.Lock()
			require.Len(t, e.sd.gens, 1)
			gen := *e.sd.gens[0]
			e.sd.mu.Unlock()
			assert.Equal(t, tc.wantMode, gen.cacheMode)
			assert.Equal(t, tc.option, gen.cacheOpt)
			if tc.wantInfo {
				assert.Equal(t, tc.mode, info["cache_mode"])
			} else {
				assert.NotContains(t, info, "cache_mode")
			}
		})
	}
}

func TestJobsCacheModeRejectsBadValues(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		option string
	}{
		{"unknown mode", "turbo", ""},
		{"option without mode", "", "threshold=0.2"},
		{"option with a flag injection", "easycache", "threshold=0.2 --offload-to-cpu"},
		{"option without value", "dbcache", "threshold"},
		{"option with a value that is not a number", "dbcache", "threshold=."},
		{"option with only signs", "easycache", "threshold=+-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newJcEnv(t)
			e.f.SetLeaser(nil)
			body := jcTxt("test", "", "sdxl_base.safetensors", "a cat")
			params := body["params"].(map[string]any)
			if tc.mode != "" {
				params["cache_mode"] = tc.mode
			}
			params["cache_option"] = tc.option
			rec := jcDo(t, e.f, http.MethodPost, "/jobs", body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_params", jcErrCode(t, rec))
			e.sd.mu.Lock()
			assert.Empty(t, e.sd.gens)
			e.sd.mu.Unlock()
		})
	}
}
