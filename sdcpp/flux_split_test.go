package sdcpp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fluxDirs struct {
	cfg    Config
	model  string
	custom string
	clipL  string
	t5     string
	gemma  string
	vae    string
}

func newFluxDirs(t *testing.T) fluxDirs {
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
	return fluxDirs{
		cfg:    cfg,
		model:  touch(t, filepath.Join(cfg.ModelsDir, "flux1-dev-fp8.safetensors")),
		custom: touch(t, filepath.Join(cfg.ModelsDir, "custom_dit.safetensors")),
		clipL:  touch(t, filepath.Join(cfg.TextEncoderDir, "clip_l.safetensors")),
		t5:     touch(t, filepath.Join(cfg.TextEncoderDir, "t5xxl_fp8_e4m3fn.safetensors")),
		gemma:  touch(t, filepath.Join(cfg.TextEncoderDir, "t5gemma_2b.safetensors")),
		vae:    touch(t, filepath.Join(cfg.VAEDir, "ae.safetensors")),
	}
}

func TestModelArgs_FluxClipLT5xxlVAEAnyOrder(t *testing.T) {
	d := newFluxDirs(t)
	names := []string{"clip_l.safetensors", "t5xxl_fp8_e4m3fn.safetensors", "ae.safetensors"}
	orders := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	want := []string{"--diffusion-model", d.model, "--clip_l", d.clipL, "--t5xxl", d.t5, "--vae", d.vae}
	for _, o := range orders {
		mods := []string{names[o[0]], names[o[1]], names[o[2]]}
		t.Run(strings.Join(mods, ","), func(t *testing.T) {
			args, err := ModelArgs(d.cfg, "flux1-dev-fp8.safetensors", mods)
			require.NoError(t, err)
			assert.Equal(t, want, args)
		})
	}
	t.Run("absolute module paths", func(t *testing.T) {
		args, err := ModelArgs(d.cfg, "flux1-dev-fp8.safetensors", []string{d.vae, d.t5, d.clipL})
		require.NoError(t, err)
		assert.Equal(t, want, args)
	})
}

func TestModelArgs_FluxT5xxlOnlyWithVAE(t *testing.T) {
	d := newFluxDirs(t)
	for _, mods := range [][]string{
		{"t5xxl_fp8_e4m3fn.safetensors", "ae.safetensors"},
		{"ae.safetensors", "t5xxl_fp8_e4m3fn.safetensors"},
	} {
		t.Run(strings.Join(mods, ","), func(t *testing.T) {
			args, err := ModelArgs(d.cfg, "flux1-dev-fp8.safetensors", mods)
			require.NoError(t, err)
			assert.Equal(t, []string{"--diffusion-model", d.model, "--t5xxl", d.t5, "--vae", d.vae}, args)
		})
	}
}

func TestModelArgs_FluxEncodersWithoutVAEFails(t *testing.T) {
	d := newFluxDirs(t)
	args, err := ModelArgs(d.cfg, "flux1-dev-fp8.safetensors", []string{"clip_l.safetensors", "t5xxl_fp8_e4m3fn.safetensors"})
	require.Error(t, err)
	assert.NotEmpty(t, err.Error())
	assert.Empty(t, args)
}

func TestModelArgs_T5GemmaGoesToLLM(t *testing.T) {
	d := newFluxDirs(t)
	cases := []struct {
		checkpoint string
		model      string
	}{
		{"flux1-dev-fp8.safetensors", d.model},
		{"custom_dit.safetensors", d.custom},
	}
	for _, tc := range cases {
		t.Run(tc.checkpoint, func(t *testing.T) {
			args, err := ModelArgs(d.cfg, tc.checkpoint, []string{"t5gemma_2b.safetensors", "ae.safetensors"})
			require.NoError(t, err)
			got := flagValues(t, args)
			assert.NotContains(t, got, "--t5xxl")
			assert.Equal(t, map[string]string{"--diffusion-model": tc.model, "--llm": d.gemma, "--vae": d.vae}, got)
		})
	}
}
