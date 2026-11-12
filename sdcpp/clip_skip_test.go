package sdcpp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneration_ClipSkipForgeDefaultNotSent(t *testing.T) {
	cases := []struct {
		name     string
		clipSkip any
		want     any
	}{
		{"absent", nil, nil},
		{"zero is forge default", 0, nil},
		{"one is forge default", 1, nil},
		{"two passed as is", 2, float64(2)},
		{"three passed as is", 3, float64(3)},
	}
	for _, path := range []string{"/sdapi/v1/txt2img", "/sdapi/v1/img2img"} {
		for _, tc := range cases {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				e := newEnv(t)
				req := map[string]any{"prompt": "a cat", "seed": 1}
				if tc.clipSkip != nil {
					req["clip_skip"] = tc.clipSkip
				}
				if path == "/sdapi/v1/img2img" {
					req["init_images"] = []string{pngB64(t, 8, 8)}
				}
				rec := e.do(t, http.MethodPost, path, req)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				bodies := e.sd.imgGenBodies()
				require.Len(t, bodies, 1)
				got, present := bodies[0]["clip_skip"]
				if tc.want == nil {
					assert.False(t, present, "clip_skip must not be sent, got %v", got)
				} else {
					assert.Equal(t, tc.want, got)
				}
			})
		}
	}
}
