package sdcpp

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelArgs_VisionModule(t *testing.T) {
	d := newMultiDirs(t)
	qwen := touch(t, filepath.Join(d.cfg.ModelsDir, "qwen_image_2.1-Q8_0.gguf"))
	qwenTE := touch(t, filepath.Join(d.cfg.TextEncoderDir, "Qwen3VL-8B-Instruct-Q4_K_M.gguf"))
	qwenVAE := touch(t, filepath.Join(d.cfg.VAEDir, "qwen_image_2.1_vae_bf16.safetensors"))
	mmproj := touch(t, filepath.Join(d.cfg.TextEncoderDir, "mmproj-Qwen3VL-8B-Instruct-F16.gguf"))

	te := "Qwen3VL-8B-Instruct-Q4_K_M.gguf"
	vae := "qwen_image_2.1_vae_bf16.safetensors"
	mm := "mmproj-Qwen3VL-8B-Instruct-F16.gguf"
	withVision := map[string]string{"--diffusion-model": qwen, "--llm": qwenTE, "--vae": qwenVAE, "--llm_vision": mmproj}
	withoutVision := map[string]string{"--diffusion-model": qwen, "--llm": qwenTE, "--vae": qwenVAE}

	cases := []struct {
		name       string
		checkpoint string
		modules    []string
		want       map[string]string
	}{
		{"mmproj last", "qwen_image_2.1-Q8_0", []string{te, vae, mm}, withVision},
		{"mmproj first", "qwen_image_2.1-Q8_0", []string{mm, te, vae}, withVision},
		{"mmproj middle", "qwen_image_2.1-Q8_0.gguf", []string{te, mm, vae}, withVision},
		{"mmproj before vae and encoder", "qwen_image_2.1-Q8_0", []string{vae, mm, te}, withVision},
		{"absolute module paths", "qwen_image_2.1-Q8_0", []string{mmproj, qwenTE, qwenVAE}, withVision},
		{"without mmproj", "qwen_image_2.1-Q8_0", []string{te, vae}, withoutVision},
		{"without mmproj reversed", "qwen_image_2.1-Q8_0", []string{vae, te}, withoutVision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := ModelArgs(d.cfg, tc.checkpoint, tc.modules)
			require.NoError(t, err)
			assert.Equal(t, tc.want, flagValues(t, args))
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "--llm" {
					assert.NotEqual(t, mmproj, args[i+1], "mmproj must never be passed as --llm: %v", args)
				}
			}
		})
	}
}

func TestModelArgs_SingleFileHiDream(t *testing.T) {
	d := newMultiDirs(t)
	hidream := touch(t, filepath.Join(d.cfg.ModelsDir, "hidream_o1_image_dev_fp8_scaled.safetensors"))

	for _, ckpt := range []string{"hidream_o1_image_dev_fp8_scaled", "hidream_o1_image_dev_fp8_scaled.safetensors"} {
		for _, mods := range [][]string{nil, {}} {
			args, err := ModelArgs(d.cfg, ckpt, mods)
			require.NoError(t, err)
			assert.Equal(t, []string{"--model", hidream}, args, "checkpoint %q modules %v", ckpt, mods)
		}
	}
}
