package sdcpp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneration_MissingLoraIsDropped(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		prompt    string
		wantLora  []map[string]any
		wantParts []string
	}{
		{
			"txt2img only missing lora", "/sdapi/v1/txt2img",
			"a cat <lora:ghost:0.7> on a chair",
			nil,
			[]string{"a cat", "on a chair"},
		},
		{
			"txt2img missing and existing lora", "/sdapi/v1/txt2img",
			"a cat <lora:ghost:0.7> on a chair <lora:detail:0.8>",
			[]map[string]any{{"path": "detail.safetensors", "multiplier": 0.8}},
			[]string{"a cat", "on a chair"},
		},
		{
			"img2img missing lora", "/sdapi/v1/img2img",
			"edit <lora:ghost:1.1> photo",
			nil,
			[]string{"edit", "photo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			req := map[string]any{"prompt": tc.prompt, "seed": 1}
			if tc.path == "/sdapi/v1/img2img" {
				req["init_images"] = []string{pngB64(t, 8, 8)}
			}
			rec := e.do(t, http.MethodPost, tc.path, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			bodies := e.sd.imgGenBodies()
			require.Len(t, bodies, 1, "request must reach sd-server")

			prompt, _ := bodies[0]["prompt"].(string)
			assert.NotContains(t, prompt, "<lora")
			assert.NotContains(t, prompt, "ghost")
			for _, part := range tc.wantParts {
				assert.Contains(t, prompt, part)
			}

			got := []map[string]any{}
			if v, present := bodies[0]["lora"]; present && v != nil {
				raw, ok := v.([]any)
				require.True(t, ok, "lora must be an array: %v", v)
				for _, r := range raw {
					m, ok := r.(map[string]any)
					require.True(t, ok, "lora entry must be an object: %v", r)
					p, _ := m["path"].(string)
					assert.NotContains(t, strings.ToLower(p), "ghost", "missing lora must not be sent")
					got = append(got, map[string]any{"path": m["path"], "multiplier": m["multiplier"]})
				}
			}
			if tc.wantLora == nil {
				assert.Empty(t, got)
			} else {
				assert.ElementsMatch(t, tc.wantLora, got)
			}
		})
	}
}
