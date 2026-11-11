package sdcpp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type multiDirs struct {
	cfg        Config
	anima      string
	klein      string
	moss       string
	sdxl       string
	animaTE    string
	animaVAE   string
	kleinTE    string
	kleinVAE   string
	mossTE     string
	mossVAE    string
	mossTokTE  string
	mossTokDef string
}

func newMultiDirs(t *testing.T) multiDirs {
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
	return multiDirs{
		cfg:        cfg,
		anima:      touch(t, filepath.Join(cfg.ModelsDir, "anima-aesthetic-v1.1.safetensors")),
		klein:      touch(t, filepath.Join(cfg.ModelsDir, "flux3-image-klein-Q8_0.gguf")),
		moss:       touch(t, filepath.Join(cfg.ModelsDir, "moss_image_sft_bf16.safetensors")),
		sdxl:       touch(t, filepath.Join(cfg.ModelsDir, "sdxl_base.safetensors")),
		animaTE:    touch(t, filepath.Join(cfg.TextEncoderDir, "qwen_3_06b_base.safetensors")),
		animaVAE:   touch(t, filepath.Join(cfg.VAEDir, "qwen_image_vae.safetensors")),
		kleinTE:    touch(t, filepath.Join(cfg.TextEncoderDir, "Qwen3VL-4B-Instruct-Q4_K_M.gguf")),
		kleinVAE:   touch(t, filepath.Join(cfg.VAEDir, "flux2-klein-vae.safetensors")),
		mossTE:     touch(t, filepath.Join(cfg.TextEncoderDir, "qwen_3_4b.safetensors")),
		mossVAE:    touch(t, filepath.Join(cfg.VAEDir, "flux2-vae.safetensors")),
		mossTokTE:  touch(t, filepath.Join(cfg.TextEncoderDir, "moss.tokenizer.json")),
		mossTokDef: touch(t, filepath.Join(cfg.TokenizerDir, "moss.tokenizer.json")),
	}
}

func TestModelArgs_MultiFile(t *testing.T) {
	d := newMultiDirs(t)

	cases := []struct {
		name       string
		checkpoint string
		modules    []string
		want       map[string]string
	}{
		{
			"anima split by text encoder, module names",
			"anima-aesthetic-v1.1",
			[]string{"qwen_3_06b_base.safetensors", "qwen_image_vae.safetensors"},
			map[string]string{"--diffusion-model": d.anima, "--llm": d.animaTE, "--vae": d.animaVAE},
		},
		{
			"anima with extension",
			"anima-aesthetic-v1.1.safetensors",
			[]string{"qwen_image_vae.safetensors", "qwen_3_06b_base.safetensors"},
			map[string]string{"--diffusion-model": d.anima, "--llm": d.animaTE, "--vae": d.animaVAE},
		},
		{
			"anima absolute module paths inside VAEDir and TextEncoderDir",
			"anima-aesthetic-v1.1",
			[]string{d.animaTE, d.animaVAE},
			map[string]string{"--diffusion-model": d.anima, "--llm": d.animaTE, "--vae": d.animaVAE},
		},
		{
			"klein gguf model with gguf encoder",
			"flux3-image-klein-Q8_0",
			[]string{"Qwen3VL-4B-Instruct-Q4_K_M.gguf", "flux2-klein-vae.safetensors"},
			map[string]string{"--diffusion-model": d.klein, "--llm": d.kleinTE, "--vae": d.kleinVAE},
		},
		{
			"klein absolute module paths",
			"flux3-image-klein-Q8_0.gguf",
			[]string{d.kleinTE, d.kleinVAE},
			map[string]string{"--diffusion-model": d.klein, "--llm": d.kleinTE, "--vae": d.kleinVAE},
		},
		{
			"moss with json module uses it as tokenizer",
			"moss_image_sft_bf16",
			[]string{"qwen_3_4b.safetensors", "flux2-vae.safetensors", "moss.tokenizer.json"},
			map[string]string{"--diffusion-model": d.moss, "--llm": d.mossTE, "--vae": d.mossVAE, "--tokenizer": d.mossTokTE},
		},
		{
			"moss json module first",
			"moss_image_sft_bf16",
			[]string{"moss.tokenizer.json", "qwen_3_4b.safetensors", "flux2-vae.safetensors"},
			map[string]string{"--diffusion-model": d.moss, "--llm": d.mossTE, "--vae": d.mossVAE, "--tokenizer": d.mossTokTE},
		},
		{
			"moss without json module uses TokenizerDir",
			"moss_image_sft_bf16",
			[]string{"qwen_3_4b.safetensors", "flux2-vae.safetensors"},
			map[string]string{"--diffusion-model": d.moss, "--llm": d.mossTE, "--vae": d.mossVAE, "--tokenizer": d.mossTokDef},
		},
		{
			"sdxl without modules",
			"sdxl_base.safetensors",
			nil,
			map[string]string{"--model": d.sdxl},
		},
		{
			"sdxl with empty modules",
			"sdxl_base",
			[]string{},
			map[string]string{"--model": d.sdxl},
		},
		{
			"sdxl with vae only",
			"sdxl_base.safetensors",
			[]string{"flux2-vae.safetensors"},
			map[string]string{"--model": d.sdxl, "--vae": d.mossVAE},
		},
		{
			"sdxl with absolute vae only",
			"sdxl_base.safetensors",
			[]string{d.animaVAE},
			map[string]string{"--model": d.sdxl, "--vae": d.animaVAE},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := ModelArgs(d.cfg, tc.checkpoint, tc.modules)
			require.NoError(t, err)
			got := flagValues(t, args)
			assert.Equal(t, tc.want, got)
			if p, ok := got["--llm"]; ok {
				assert.NotEqual(t, ".json", filepath.Ext(p), "json must not be passed as --llm")
			}
		})
	}
}

func TestModelArgs_MultiFile_KleinGGUFPath(t *testing.T) {
	d := newMultiDirs(t)
	args, err := ModelArgs(d.cfg, "flux3-image-klein-Q8_0", []string{"Qwen3VL-4B-Instruct-Q4_K_M.gguf", "flux2-klein-vae.safetensors"})
	require.NoError(t, err)
	got := flagValues(t, args)
	assert.Equal(t, ".gguf", filepath.Ext(got["--diffusion-model"]))
	assert.NotContains(t, got, "--model")
}

func TestModelArgs_AnimaExactArgs(t *testing.T) {
	d := newMultiDirs(t)
	args, err := ModelArgs(d.cfg, "anima-aesthetic-v1.1", []string{"qwen_3_06b_base.safetensors", "qwen_image_vae.safetensors"})
	require.NoError(t, err)
	assert.Equal(t, []string{"--diffusion-model", d.anima, "--llm", d.animaTE, "--vae", d.animaVAE}, args)
	assert.NotContains(t, args, "--tokenizer")
}

func TestModelArgs_MultiFile_Errors(t *testing.T) {
	d := newMultiDirs(t)
	outside := t.TempDir()
	outTE := touch(t, filepath.Join(outside, "qwen_3_06b_base.safetensors"))
	outVAE := touch(t, filepath.Join(outside, "qwen_image_vae.safetensors"))

	cases := []struct {
		name       string
		checkpoint string
		modules    []string
	}{
		{"non-klein encoder without vae", "anima-aesthetic-v1.1", []string{"qwen_3_06b_base.safetensors"}},
		{"sdxl encoder without vae", "sdxl_base.safetensors", []string{d.animaTE}},
		{"klein without modules", "flux3-image-klein-Q8_0", nil},
		{"moss without modules", "moss_image_sft_bf16", nil},
		{"absolute encoder outside folders", "anima-aesthetic-v1.1", []string{outTE, d.animaVAE}},
		{"absolute vae outside folders", "anima-aesthetic-v1.1", []string{d.animaTE, outVAE}},
		{"absolute vae outside folders for sdxl", "sdxl_base.safetensors", []string{outVAE}},
		{"relative traversal out of folders", "anima-aesthetic-v1.1", []string{"../../" + filepath.Base(outside) + "/qwen_3_06b_base.safetensors", "qwen_image_vae.safetensors"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ModelArgs(d.cfg, tc.checkpoint, tc.modules)
			require.Error(t, err)
			assert.NotEmpty(t, err.Error())
		})
	}
}
