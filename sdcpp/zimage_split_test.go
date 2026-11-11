package sdcpp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type zimageDirs struct {
	cfg  Config
	gguf string
	aio  string
	te   string
	vae  string
}

func newZImageDirs(t *testing.T) zimageDirs {
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
	return zimageDirs{
		cfg:  cfg,
		gguf: touch(t, filepath.Join(cfg.ModelsDir, "z_image_turbo-Q8_0.gguf")),
		aio:  touch(t, filepath.Join(cfg.ModelsDir, "z-image-turbo-aio.safetensors")),
		te:   touch(t, filepath.Join(cfg.TextEncoderDir, "qwen_3_4b.safetensors")),
		vae:  touch(t, filepath.Join(cfg.VAEDir, "ae.safetensors")),
	}
}

func TestModelArgs_ZImageSplitGGUF(t *testing.T) {
	d := newZImageDirs(t)
	cases := []struct {
		name    string
		modules []string
	}{
		{"encoder then vae", []string{"qwen_3_4b.safetensors", "ae.safetensors"}},
		{"vae then encoder", []string{"ae.safetensors", "qwen_3_4b.safetensors"}},
		{"absolute module paths", []string{d.te, d.vae}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := ModelArgs(d.cfg, "z_image_turbo-Q8_0.gguf", tc.modules)
			require.NoError(t, err)
			assert.Equal(t, []string{"--diffusion-model", d.gguf, "--llm", d.te, "--vae", d.vae}, args)
		})
	}
}

func TestModelArgs_ZImageAIORejected(t *testing.T) {
	d := newZImageDirs(t)
	args, err := ModelArgs(d.cfg, "z-image-turbo-aio.safetensors", []string{"qwen_3_4b.safetensors", "ae.safetensors"})
	require.Error(t, err)
	assert.NotEmpty(t, err.Error())
	assert.Empty(t, args)
}
