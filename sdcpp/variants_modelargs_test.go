package sdcpp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type variantDirs struct {
	cfg    Config
	zGGUF  string
	zSafe  string
	chroma string
	flux   string
	zTE    string
	ae     string
	t5     string
	clipL  string
}

func newVariantDirs(t *testing.T) variantDirs {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		ModelsDir:      filepath.Join(root, "Stable-diffusion"),
		LoraDir:        filepath.Join(root, "Lora"),
		VAEDir:         filepath.Join(root, "VAE"),
		TextEncoderDir: filepath.Join(root, "text_encoder"),
		UpscalerDir:    filepath.Join(root, "ESRGAN"),
		TokenizerDir:   filepath.Join(root, "tokenizer"),
	}
	for _, d := range []string{cfg.ModelsDir, cfg.LoraDir, cfg.VAEDir, cfg.TextEncoderDir, cfg.UpscalerDir, cfg.TokenizerDir} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	return variantDirs{
		cfg:    cfg,
		zGGUF:  touch(t, filepath.Join(cfg.ModelsDir, "z-image-turbo-fp8-aio.gguf")),
		zSafe:  touch(t, filepath.Join(cfg.ModelsDir, "z-image-turbo-fp8-aio.safetensors")),
		chroma: touch(t, filepath.Join(cfg.ModelsDir, "Chroma1-HD.gguf")),
		flux:   touch(t, filepath.Join(cfg.ModelsDir, "flux1-dev-fp8.gguf")),
		zTE:    touch(t, filepath.Join(cfg.TextEncoderDir, "Qwen3-4B-Q8_0.gguf")),
		ae:     touch(t, filepath.Join(cfg.VAEDir, "ae.safetensors")),
		t5:     touch(t, filepath.Join(cfg.TextEncoderDir, "t5xxl_fp8_e4m3fn.safetensors")),
		clipL:  touch(t, filepath.Join(cfg.TextEncoderDir, "clip_l.safetensors")),
	}
}

func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestModelArgs_ZImageAIONameWithModules(t *testing.T) {
	d := newVariantDirs(t)
	cases := []struct {
		name       string
		checkpoint string
		modules    []string
		path       string
	}{
		{"gguf variant", "z-image-turbo-fp8-aio.gguf", []string{"Qwen3-4B-Q8_0.gguf", "ae.safetensors"}, d.zGGUF},
		{"gguf variant modules reversed", "z-image-turbo-fp8-aio.gguf", []string{"ae.safetensors", "Qwen3-4B-Q8_0.gguf"}, d.zGGUF},
		{"bf16 safetensors variant", "z-image-turbo-fp8-aio.safetensors", []string{"Qwen3-4B-Q8_0.gguf", "ae.safetensors"}, d.zSafe},
		{"absolute module paths", "z-image-turbo-fp8-aio.gguf", []string{d.zTE, d.ae}, d.zGGUF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := ModelArgs(d.cfg, tc.checkpoint, tc.modules)
			require.NoError(t, err)
			require.NotEmpty(t, args)
			assert.Equal(t, "--diffusion-model", args[0])
			assert.Equal(t, map[string]string{"--diffusion-model": tc.path, "--llm": d.zTE, "--vae": d.ae}, flagValues(t, args))
		})
	}
}

func TestModelArgs_ZImageWithoutModulesRejected(t *testing.T) {
	d := newVariantDirs(t)
	for _, ckpt := range []string{"z-image-turbo-fp8-aio.gguf", "z-image-turbo-fp8-aio.safetensors", "z-image-turbo-fp8-aio"} {
		for _, mods := range [][]string{nil, {}} {
			args, err := ModelArgs(d.cfg, ckpt, mods)
			assert.Error(t, err, "checkpoint %q modules %v", ckpt, mods)
			assert.Empty(t, args)
		}
	}
}

func TestModelArgs_ChromaModelArgs(t *testing.T) {
	d := newVariantDirs(t)
	cases := []struct {
		name    string
		modules []string
	}{
		{"t5 then vae", []string{"t5xxl_fp8_e4m3fn.safetensors", "ae.safetensors"}},
		{"vae then t5", []string{"ae.safetensors", "t5xxl_fp8_e4m3fn.safetensors"}},
		{"absolute paths", []string{d.t5, d.ae}},
	}
	for _, tc := range cases {
		for _, ckpt := range []string{"Chroma1-HD.gguf", "Chroma1-HD"} {
			t.Run(tc.name+"/"+ckpt, func(t *testing.T) {
				args, err := ModelArgs(d.cfg, ckpt, tc.modules)
				require.NoError(t, err)
				assert.True(t, hasPair(args, "--diffusion-model", d.chroma), "%v", args)
				assert.True(t, hasPair(args, "--t5xxl", d.t5), "%v", args)
				assert.True(t, hasPair(args, "--vae", d.ae), "%v", args)
				assert.True(t, hasPair(args, "--model-args", "chroma_use_dit_mask=false"), "%v", args)
				assert.NotContains(t, args, "--clip_l")
			})
		}
	}
}

func TestModelArgs_FluxWithoutChromaHasNoModelArgs(t *testing.T) {
	d := newVariantDirs(t)
	args, err := ModelArgs(d.cfg, "flux1-dev-fp8.gguf", []string{"clip_l.safetensors", "t5xxl_fp8_e4m3fn.safetensors", "ae.safetensors"})
	require.NoError(t, err)
	assert.True(t, hasPair(args, "--diffusion-model", d.flux), "%v", args)
	assert.True(t, hasPair(args, "--clip_l", d.clipL), "%v", args)
	assert.True(t, hasPair(args, "--t5xxl", d.t5), "%v", args)
	assert.True(t, hasPair(args, "--vae", d.ae), "%v", args)
	assert.NotContains(t, args, "--model-args")
}
