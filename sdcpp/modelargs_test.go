package sdcpp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type modelDirs struct {
	cfg Config
	te  string
	vae string
}

func touch(t *testing.T, path string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	return path
}

func newModelDirs(t *testing.T) modelDirs {
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
	touch(t, filepath.Join(cfg.ModelsDir, "sdxl_base.safetensors"))
	touch(t, filepath.Join(cfg.ModelsDir, "flux1-dev.gguf"))
	touch(t, filepath.Join(cfg.ModelsDir, "old_sd15.ckpt"))
	touch(t, filepath.Join(cfg.ModelsDir, "flux-2-klein-4b.safetensors"))
	touch(t, filepath.Join(cfg.ModelsDir, "FLUX-2-Klein-9B.safetensors"))
	touch(t, filepath.Join(cfg.ModelsDir, "moss_v1.safetensors"))
	touch(t, filepath.Join(cfg.ModelsDir, "z-image-turbo-aio.safetensors"))
	touch(t, filepath.Join(cfg.ModelsDir, "zimage_turbo.safetensors"))
	touch(t, filepath.Join(cfg.TokenizerDir, "moss.tokenizer.json"))
	te := touch(t, filepath.Join(cfg.TextEncoderDir, "qwen_3_4b.safetensors"))
	vae := touch(t, filepath.Join(cfg.VAEDir, "flux2-vae.safetensors"))
	return modelDirs{cfg: cfg, te: te, vae: vae}
}

func flagValues(t *testing.T, args []string) map[string]string {
	t.Helper()
	require.Equal(t, 0, len(args)%2, "args must be flag/value pairs: %v", args)
	m := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		_, dup := m[args[i]]
		require.False(t, dup, "duplicate flag %s in %v", args[i], args)
		m[args[i]] = args[i+1]
	}
	return m
}

func TestModelArgs(t *testing.T) {
	d := newModelDirs(t)
	plainVAE := touch(t, filepath.Join(d.cfg.VAEDir, "ae.safetensors"))
	otherDir := filepath.Join(d.cfg.ModelsDir, "extra")
	namedVAE := touch(t, filepath.Join(otherDir, "flux2_vae.safetensors"))
	otherTE := touch(t, filepath.Join(otherDir, "qwen3-8b.gguf"))
	md := d.cfg.ModelsDir

	cases := []struct {
		name       string
		checkpoint string
		modules    []string
		want       map[string]string
	}{
		{"sdxl single file", "sdxl_base.safetensors", nil, map[string]string{"--model": filepath.Join(md, "sdxl_base.safetensors")}},
		{"checkpoint with hash", "sdxl_base.safetensors [31e35c80fc]", nil, map[string]string{"--model": filepath.Join(md, "sdxl_base.safetensors")}},
		{"no extension finds safetensors", "sdxl_base", nil, map[string]string{"--model": filepath.Join(md, "sdxl_base.safetensors")}},
		{"no extension finds gguf", "flux1-dev", nil, map[string]string{"--model": filepath.Join(md, "flux1-dev.gguf")}},
		{"no extension finds ckpt", "old_sd15", nil, map[string]string{"--model": filepath.Join(md, "old_sd15.ckpt")}},
		{"flux1 gguf", "flux1-dev.gguf", nil, map[string]string{"--model": filepath.Join(md, "flux1-dev.gguf")}},
		{
			"klein", "flux-2-klein-4b.safetensors", []string{d.te, d.vae},
			map[string]string{"--diffusion-model": filepath.Join(md, "flux-2-klein-4b.safetensors"), "--llm": d.te, "--vae": d.vae},
		},
		{
			"klein modules reversed", "flux-2-klein-4b.safetensors", []string{d.vae, d.te},
			map[string]string{"--diffusion-model": filepath.Join(md, "flux-2-klein-4b.safetensors"), "--llm": d.te, "--vae": d.vae},
		},
		{
			"klein case-insensitive", "FLUX-2-Klein-9B.safetensors", []string{d.te, d.vae},
			map[string]string{"--diffusion-model": filepath.Join(md, "FLUX-2-Klein-9B.safetensors"), "--llm": d.te, "--vae": d.vae},
		},
		{
			"klein vae detected by VAEDir", "flux-2-klein-4b.safetensors", []string{plainVAE, d.te},
			map[string]string{"--diffusion-model": filepath.Join(md, "flux-2-klein-4b.safetensors"), "--llm": d.te, "--vae": plainVAE},
		},
		{
			"klein vae detected by name outside VAEDir", "flux-2-klein-4b.safetensors", []string{otherTE, namedVAE},
			map[string]string{"--diffusion-model": filepath.Join(md, "flux-2-klein-4b.safetensors"), "--llm": otherTE, "--vae": namedVAE},
		},
		{
			"klein with hash", "flux-2-klein-4b.safetensors [deadbeef]", []string{d.te, d.vae},
			map[string]string{"--diffusion-model": filepath.Join(md, "flux-2-klein-4b.safetensors"), "--llm": d.te, "--vae": d.vae},
		},
		{
			"moss adds tokenizer", "moss_v1.safetensors", []string{d.te, d.vae},
			map[string]string{
				"--diffusion-model": filepath.Join(md, "moss_v1.safetensors"),
				"--llm":             d.te,
				"--vae":             d.vae,
				"--tokenizer":       filepath.Join(d.cfg.TokenizerDir, "moss.tokenizer.json"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := ModelArgs(d.cfg, tc.checkpoint, tc.modules)
			require.NoError(t, err)
			assert.Equal(t, tc.want, flagValues(t, args))
		})
	}
}

func TestModelArgs_Errors(t *testing.T) {
	d := newModelDirs(t)

	cases := []struct {
		name       string
		checkpoint string
		modules    []string
	}{
		{"klein without modules", "flux-2-klein-4b.safetensors", nil},
		{"klein without vae", "flux-2-klein-4b.safetensors", []string{d.te}},
		{"klein without text encoder", "flux-2-klein-4b.safetensors", []string{d.vae}},
		{"moss without vae", "moss_v1.safetensors", []string{d.te}},
		{"moss without text encoder", "moss_v1.safetensors", []string{d.vae}},
		{"z-image without alias", "z-image-turbo-aio.safetensors", nil},
		{"zimage without alias", "zimage_turbo.safetensors", nil},
		{"z-image without alias with modules", "z-image-turbo-aio.safetensors", []string{d.te, d.vae}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ModelArgs(d.cfg, tc.checkpoint, tc.modules)
			require.Error(t, err)
			assert.NotEmpty(t, err.Error())
		})
	}
}

func TestModelArgs_Alias(t *testing.T) {
	d := newModelDirs(t)
	alias := []string{"--diffusion-model", "/m/z_image_turbo.gguf", "--llm", "/m/qwen_3_4b.gguf", "--vae", "/m/ae.safetensors"}
	d.cfg.Aliases = map[string][]string{
		"z-image-turbo-aio.safetensors": alias,
		"sdxl_base.safetensors":         {"--model", "/elsewhere/sdxl.gguf"},
	}

	cases := []struct {
		name       string
		checkpoint string
		want       []string
	}{
		{"z-image alias", "z-image-turbo-aio.safetensors", alias},
		{"alias wins over computed args", "sdxl_base.safetensors", []string{"--model", "/elsewhere/sdxl.gguf"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := ModelArgs(d.cfg, tc.checkpoint, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.want, args)
		})
	}

	t.Run("returns a copy", func(t *testing.T) {
		args, err := ModelArgs(d.cfg, "z-image-turbo-aio.safetensors", nil)
		require.NoError(t, err)
		args[1] = "mutated"
		again, err := ModelArgs(d.cfg, "z-image-turbo-aio.safetensors", nil)
		require.NoError(t, err)
		assert.Equal(t, "/m/z_image_turbo.gguf", again[1])
		assert.Equal(t, "/m/z_image_turbo.gguf", d.cfg.Aliases["z-image-turbo-aio.safetensors"][1])
	})
}

func TestModelArgs_ExcludesBaseArgs(t *testing.T) {
	d := newModelDirs(t)
	d.cfg.BaseArgs = []string{"--listen-port", "7861"}
	args, err := ModelArgs(d.cfg, "sdxl_base.safetensors", nil)
	require.NoError(t, err)
	assert.NotContains(t, args, "--listen-port")
}

func TestSamplerName(t *testing.T) {
	cases := []struct {
		forge string
		want  string
		ok    bool
	}{
		{"Euler", "euler", true},
		{"Euler a", "euler_a", true},
		{"DPM++ 2M", "dpm++2m", true},
		{"", "", true},
		{"Definitely Not A Sampler", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.forge, func(t *testing.T) {
			got, ok := SamplerName(tc.forge)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestSchedulerName(t *testing.T) {
	cases := []struct {
		forge string
		want  string
		ok    bool
	}{
		{"Simple", "simple", true},
		{"Karras", "karras", true},
		{"Automatic", "", true},
		{"", "", true},
		{"Definitely Not A Scheduler", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.forge, func(t *testing.T) {
			got, ok := SchedulerName(tc.forge)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
