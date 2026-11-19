package train

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	flux1    = "Flux.1"
	flux1AIO = "flux1-dev-fp8.safetensors"
)

func flux1Config(t *testing.T, others ...string) Config {
	t.Helper()
	cfg := configWith(t, others...)
	touch(t, filepath.Join(cfg.ModelsDir, flux1AIO))
	return cfg
}

func processSection(t *testing.T, doc any, key string) map[string]any {
	t.Helper()
	root, ok := doc.(map[string]any)
	require.True(t, ok)
	config, ok := root["config"].(map[string]any)
	require.True(t, ok, "config section")
	process, ok := config["process"].([]any)
	require.True(t, ok, "config.process")
	require.Len(t, process, 1)
	p, ok := process[0].(map[string]any)
	require.True(t, ok)
	switch v := p[key].(type) {
	case map[string]any:
		return v
	case []any:
		require.Len(t, v, 1, "process.%s", key)
		m, ok := v[0].(map[string]any)
		require.True(t, ok, "process.%s[0]", key)
		return m
	}
	t.Fatalf("process.%s missing: %v", key, p[key])
	return nil
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, path)
	require.NotEmpty(t, data, path)
	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out), path)
	return out
}

func TestFlux1_BaseListedOnlyWithFile(t *testing.T) {
	tests := []struct {
		name   string
		flux1  bool
		others []string
		want   []string
	}{
		{"only flux1 file", true, nil, []string{flux1}},
		{"no flux1 file", false, allBases, allBases},
		{"nothing", false, nil, nil},
		{"flux1 with all others", true, allBases, append(append([]string{}, allBases...), flux1)},
		{"flux1 with Flux.2", true, []string{flux2}, []string{flux2, flux1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := configWith(t, tt.others...)
			if tt.flux1 {
				touch(t, filepath.Join(cfg.ModelsDir, flux1AIO))
			}
			e := startEnv(t, cfg, nil)
			if len(tt.want) == 0 {
				e.assertBases(nil)
				return
			}
			assert.ElementsMatch(t, tt.want, e.m.Bases())
			assert.ElementsMatch(t, tt.want, e.getBases())
		})
	}
}

func TestFlux1_FileInWrongDirNotListed(t *testing.T) {
	for _, dir := range []func(Config) string{
		func(c Config) string { return c.TextEncoderDir },
		func(c Config) string { return c.VAEDir },
		func(c Config) string { return c.TrainDir },
	} {
		cfg := emptyConfig(t)
		touch(t, filepath.Join(dir(cfg), flux1AIO))
		e := startEnv(t, cfg, nil)
		assert.NotContains(t, e.m.Bases(), flux1)
		assert.NotContains(t, e.getBases(), flux1)
	}
}

func TestFlux1_OtherFluxCheckpointNotEnough(t *testing.T) {
	cfg := emptyConfig(t)
	touch(t, filepath.Join(cfg.ModelsDir, "flux1-dev.safetensors"))
	touch(t, filepath.Join(cfg.ModelsDir, "flux1-schnell-fp8.safetensors"))
	e := startEnv(t, cfg, nil)
	assert.NotContains(t, e.m.Bases(), flux1)
}

func TestFlux1_WithoutFileRejected(t *testing.T) {
	e := startEnv(t, configWith(t, allBases...), nil)
	require.NotContains(t, e.getBases(), flux1)
	rec := e.post(baseFields("anna", "woman", flux1), photos(t, 10))
	assertRejected(t, e, rec, "base_unavailable")
}

func TestFlux1_RejectedWhenDirsNotConfigured(t *testing.T) {
	cfg := testConfig(t)
	cfg.ModelsDir, cfg.TextEncoderDir, cfg.VAEDir, cfg.TrainDir = "", "", "", ""
	e := startEnv(t, cfg, nil)
	rec := e.post(baseFields("anna", "woman", flux1), photos(t, 10))
	assertRejected(t, e, rec, "base_unavailable")
}

func TestFlux1_FileGoneBeforeStartFailsJob(t *testing.T) {
	cfg := flux1Config(t, zImage)
	e := startEnv(t, cfg, nil)
	rec := e.post(baseFields("zara", "woman", zImage), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	first := decodeJob(t, rec)
	e.waitStarted()
	rec = e.post(baseFields("anna", "woman", flux1), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	queued := decodeJob(t, rec)
	require.NoError(t, os.Remove(filepath.Join(cfg.ModelsDir, flux1AIO)))
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(first.ID(), "completed")
	j := e.waitStatus(queued.ID(), "failed")
	require.NotNil(t, j.Error)
	assert.Equal(t, "base_unavailable", j.Error.Code)
	e.m.Wait()
}

func TestFlux1_ConfigQuantizedLowVRAM(t *testing.T) {
	cfg := flux1Config(t)
	e := startEnv(t, cfg, nil)
	s := e.startJob(baseFields("anna", "woman", flux1))

	assert.Equal(t, flux1, s.base)
	assertJobFiles(t, s)

	model := processSection(t, s.doc, "model")
	assert.Equal(t, true, model["is_flux"])
	assert.Equal(t, true, model["quantize"])
	assert.Equal(t, "qfloat8", model["qtype"])
	assert.Equal(t, false, model["quantize_te"])
	assert.Equal(t, true, model["low_vram"])

	dataset := processSection(t, s.doc, "datasets")
	assert.Equal(t, true, dataset["cache_text_embeddings"])
	assert.Equal(t, []int{512}, ints(dataset["resolution"]))

	train := processSection(t, s.doc, "train")
	assert.Equal(t, true, train["unload_text_encoder"])

	res := valuesOf(s.doc, "resolution")
	require.NotEmpty(t, res)
	for _, r := range res {
		assert.Equal(t, []int{512}, ints(r))
	}
	assert.Equal(t, flux1, jobBase(t, e.get("/api/train/jobs/"+s.job.ID())))
	e.finishJob(s.job.ID())
}

func TestFlux1_ConfigUsesLocalFilesOnly(t *testing.T) {
	cfg := flux1Config(t)
	e := startEnv(t, cfg, nil)
	s := e.startJob(baseFields("anna", "woman", flux1))

	assert.NotContains(t, s.raw, "black-forest-labs")
	assert.NotContains(t, s.raw, "huggingface")
	assert.NotContains(t, s.raw, "ostris/")

	model := processSection(t, s.doc, "model")
	dir, ok := model["name_or_path"].(string)
	require.True(t, ok, "model.name_or_path must be a string")
	require.True(t, filepath.IsAbs(dir), "name_or_path must be absolute: %s", dir)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.IsDir(), "name_or_path must be a directory: %s", dir)

	for _, rel := range []string{
		"transformer/config.json",
		"vae/config.json",
		"scheduler/scheduler_config.json",
		"text_encoder/config.json",
		"text_encoder_2/config.json",
		"tokenizer/vocab.json",
		"tokenizer/merges.txt",
		"tokenizer_2/spiece.model",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		fi, err := os.Stat(p)
		if assert.NoError(t, err, rel) {
			assert.Positive(t, fi.Size(), "%s must not be empty", rel)
		}
	}

	transformer := readJSONFile(t, filepath.Join(dir, "transformer", "config.json"))
	assert.Equal(t, true, transformer["guidance_embeds"])
	vae := readJSONFile(t, filepath.Join(dir, "vae", "config.json"))
	assert.EqualValues(t, 16, vae["latent_channels"])
	for _, rel := range []string{"scheduler/scheduler_config.json", "text_encoder/config.json", "text_encoder_2/config.json", "tokenizer/vocab.json"} {
		readJSONFile(t, filepath.Join(dir, filepath.FromSlash(rel)))
	}

	assert.Equal(t, filepath.Join(cfg.ModelsDir, flux1AIO), s.local["aio"])
	aio, ok := s.local["aio"].(string)
	require.True(t, ok)
	assert.True(t, filepath.IsAbs(aio))
	e.finishJob(s.job.ID())
}

func TestFlux1_RelativeModelsDirBecomesAbsolute(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	setModelDirs(t, &cfg, root)
	touch(t, filepath.Join(cfg.ModelsDir, flux1AIO))
	t.Chdir(root)
	cfg.ModelsDir = "checkpoints"
	e := startEnv(t, cfg, nil)
	s := e.startJob(baseFields("anna", "woman", flux1))
	assert.Equal(t, filepath.Join(root, "checkpoints", flux1AIO), s.local["aio"])
	e.finishJob(s.job.ID())
}

func TestFlux1_PersonCarriesBase(t *testing.T) {
	cfg := flux1Config(t)
	e := startEnv(t, cfg, nil)
	j, _, _ := e.trainedConfig(baseFields("anna", "woman", flux1))
	e.finishJob(j.ID())
	people := e.people()
	require.Len(t, people, 1)
	assert.Equal(t, flux1, people[0].Base)
}
