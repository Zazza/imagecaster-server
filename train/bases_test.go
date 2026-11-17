package train

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	zImage = "Z-Image"
	moss   = "Moss-Image"
	sdxl   = "SDXL"
	flux2  = "Flux.2"

	zimageCheckpoint = "z_image_turbo_bf16.safetensors"
	zimageAdapter    = "zimage_turbo_training_adapter_v2.safetensors"
	qwen3TextEnc     = "qwen_3_4b.safetensors"
	aeVAE            = "ae.safetensors"

	mossCheckpoint = "moss_image_sft_bf16.safetensors"
	mossTextEnc    = "qwen35_9b_int8_convrot.safetensors"
	vaeKlein       = "flux2-klein-vae.safetensors"
	vaeFull        = "flux2-vae.safetensors"
	vaeSmall       = "flux2-vae_small.safetensors"

	sdxlJuggernaut = "juggernautXL_ragnarokBy.safetensors"
	sdxlEpic       = "epicrealismXL_pureFix.safetensors"
	sdxlPhoton     = "photon_v1.safetensors"

	fluxCheckpoint = "flux-2-klein-4b.safetensors"
)

var allBases = []string{zImage, moss, sdxl, flux2}

func setModelDirs(t *testing.T, cfg *Config, root string) {
	t.Helper()
	cfg.ModelsDir = filepath.Join(root, "checkpoints")
	cfg.TextEncoderDir = filepath.Join(root, "text_encoders")
	cfg.VAEDir = filepath.Join(root, "vae")
	cfg.TrainDir = filepath.Join(root, "train")
	for _, d := range []string{cfg.ModelsDir, cfg.TextEncoderDir, cfg.VAEDir, cfg.TrainDir} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
}

func baseFiles(cfg Config, base string) []string {
	switch base {
	case zImage:
		return []string{
			filepath.Join(cfg.TrainDir, zimageCheckpoint),
			filepath.Join(cfg.TrainDir, zimageAdapter),
			filepath.Join(cfg.TextEncoderDir, qwen3TextEnc),
			filepath.Join(cfg.VAEDir, aeVAE),
		}
	case moss:
		return []string{
			filepath.Join(cfg.ModelsDir, mossCheckpoint),
			filepath.Join(cfg.TextEncoderDir, mossTextEnc),
			filepath.Join(cfg.VAEDir, vaeKlein),
		}
	case sdxl:
		return []string{filepath.Join(cfg.ModelsDir, sdxlJuggernaut)}
	case flux2:
		return []string{
			filepath.Join(cfg.ModelsDir, fluxCheckpoint),
			filepath.Join(cfg.TextEncoderDir, qwen3TextEnc),
			filepath.Join(cfg.VAEDir, vaeKlein),
		}
	}
	panic("unknown base " + base)
}

func addBaseFiles(t *testing.T, cfg Config, bases ...string) {
	t.Helper()
	for _, b := range bases {
		for _, f := range baseFiles(cfg, b) {
			touch(t, f)
		}
	}
}

func emptyConfig(t *testing.T) Config {
	t.Helper()
	cfg := testConfig(t)
	setModelDirs(t, &cfg, t.TempDir())
	return cfg
}

func configWith(t *testing.T, bases ...string) Config {
	t.Helper()
	cfg := emptyConfig(t)
	addBaseFiles(t, cfg, bases...)
	return cfg
}

func without(list []string, drop string) []string {
	var out []string
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

type mossFiles struct {
	checkpoint bool
	textEnc    bool
	vaes       []string
}

var allMoss = mossFiles{checkpoint: true, textEnc: true, vaes: []string{vaeKlein}}

func touch(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, nil, 0o644))
}

func mossConfig(t *testing.T, files mossFiles) Config {
	t.Helper()
	cfg := emptyConfig(t)
	if files.checkpoint {
		touch(t, filepath.Join(cfg.ModelsDir, mossCheckpoint))
	}
	if files.textEnc {
		touch(t, filepath.Join(cfg.TextEncoderDir, mossTextEnc))
	}
	for _, v := range files.vaes {
		touch(t, filepath.Join(cfg.VAEDir, v))
	}
	return cfg
}

func baseFields(name, gender, base string) map[string]string {
	f := personFields(name, gender)
	f["base"] = base
	return f
}

func jobBase(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var j struct {
		Base string `json:"base"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &j), rec.Body.String())
	return j.Base
}

func (e *env) getBases() []string {
	e.t.Helper()
	rec := e.get("/api/train/bases")
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Bases []string `json:"bases"`
	}
	require.NoError(e.t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return resp.Bases
}

func (e *env) assertBases(want []string) {
	e.t.Helper()
	if len(want) == 0 {
		assert.Empty(e.t, e.m.Bases())
		rec := e.get("/api/train/bases")
		require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
		var resp map[string]any
		require.NoError(e.t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
		assert.Equal(e.t, []any{}, resp["bases"], "empty bases must be [] not null: %s", rec.Body.String())
		return
	}
	assert.Equal(e.t, want, e.m.Bases())
	assert.Equal(e.t, want, e.getBases())
}

func readYAML(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc any
	require.NoError(t, yaml.Unmarshal(data, &doc), string(data))
	return doc
}

func collectKey(node any, key string, out *[]any) {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			if k == key {
				*out = append(*out, val)
			}
			collectKey(val, key, out)
		}
	case []any:
		for _, val := range v {
			collectKey(val, key, out)
		}
	}
}

func valuesOf(doc any, key string) []any {
	var out []any
	collectKey(doc, key, &out)
	return out
}

func onlyValue(t *testing.T, doc any, key string) any {
	t.Helper()
	vals := valuesOf(doc, key)
	require.Len(t, vals, 1, "key %s must appear once in config", key)
	return vals[0]
}

func containsString(node any, want string) bool {
	switch v := node.(type) {
	case string:
		return v == want
	case map[string]any:
		for _, val := range v {
			if containsString(val, want) {
				return true
			}
		}
	case []any:
		for _, val := range v {
			if containsString(val, want) {
				return true
			}
		}
	}
	return false
}

func ints(node any) []int {
	switch v := node.(type) {
	case int:
		return []int{v}
	case []any:
		var out []int
		for _, x := range v {
			out = append(out, ints(x)...)
		}
		return out
	}
	return nil
}

type startedJob struct {
	job   jobView
	base  string
	spec  RunSpec
	doc   any
	raw   string
	local map[string]any
}

func (e *env) startJob(fields map[string]string) startedJob {
	e.t.Helper()
	rec := e.post(fields, photos(e.t, 10))
	require.Equal(e.t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := decodeJob(e.t, rec)
	call := e.waitStarted()
	raw, err := os.ReadFile(call.spec.ConfigPath)
	require.NoError(e.t, err)
	s := startedJob{job: j, base: jobBase(e.t, rec), spec: call.spec, doc: readYAML(e.t, call.spec.ConfigPath), raw: string(raw)}
	require.NotEmpty(e.t, call.spec.LocalPath, "RunSpec.LocalPath must be set")
	data, err := os.ReadFile(call.spec.LocalPath)
	require.NoError(e.t, err)
	require.NoError(e.t, json.Unmarshal(data, &s.local), string(data))
	return s
}

func (e *env) trainedConfig(fields map[string]string) (jobView, string, any) {
	e.t.Helper()
	s := e.startJob(fields)
	return s.job, s.base, s.doc
}

func (e *env) finishJob(id string) {
	e.t.Helper()
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(id, "completed")
	e.m.Wait()
}

func noJobDirs(t *testing.T, jobsDir string) {
	t.Helper()
	entries, err := os.ReadDir(jobsDir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	require.NoError(t, err)
	for _, en := range entries {
		assert.False(t, en.IsDir(), "job dir left after rejection: %s", en.Name())
	}
}

func TestBases_OrderAndFiltering(t *testing.T) {
	tests := []struct {
		name  string
		bases []string
		want  []string
	}{
		{"all", []string{flux2, sdxl, moss, zImage}, allBases},
		{"nothing", nil, nil},
		{"only Z-Image", []string{zImage}, []string{zImage}},
		{"only Moss", []string{moss}, []string{moss}},
		{"only SDXL", []string{sdxl}, []string{sdxl}},
		{"only Flux.2", []string{flux2}, []string{flux2}},
		{"Flux.2 and Z-Image", []string{flux2, zImage}, []string{zImage, flux2}},
		{"SDXL and Moss", []string{sdxl, moss}, []string{moss, sdxl}},
		{"no Z-Image", []string{moss, sdxl, flux2}, []string{moss, sdxl, flux2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := startEnv(t, configWith(t, tt.bases...), nil)
			e.assertBases(tt.want)
		})
	}
}

func TestBases_EveryFileRequired(t *testing.T) {
	for _, base := range allBases {
		probe := emptyConfig(t)
		for i := range baseFiles(probe, base) {
			t.Run(base+"/"+filepath.Base(baseFiles(probe, base)[i]), func(t *testing.T) {
				cfg := configWith(t, base)
				require.NoError(t, os.Remove(baseFiles(cfg, base)[i]))
				e := startEnv(t, cfg, nil)
				assert.NotContains(t, e.m.Bases(), base)
				assert.NotContains(t, e.getBases(), base)
			})
		}
	}
}

func TestBases_DirsNotConfigured(t *testing.T) {
	cfg := testConfig(t)
	cfg.ModelsDir, cfg.TextEncoderDir, cfg.VAEDir, cfg.TrainDir = "", "", "", ""
	e := startEnv(t, cfg, nil)
	e.assertBases(nil)
}

func TestBases_FilesInWrongDirs(t *testing.T) {
	cfg := emptyConfig(t)
	touch(t, filepath.Join(cfg.ModelsDir, zimageCheckpoint))
	touch(t, filepath.Join(cfg.ModelsDir, zimageAdapter))
	touch(t, filepath.Join(cfg.TrainDir, qwen3TextEnc))
	touch(t, filepath.Join(cfg.TrainDir, aeVAE))
	touch(t, filepath.Join(cfg.TextEncoderDir, mossCheckpoint))
	touch(t, filepath.Join(cfg.VAEDir, mossTextEnc))
	touch(t, filepath.Join(cfg.ModelsDir, vaeKlein))
	touch(t, filepath.Join(cfg.TrainDir, sdxlJuggernaut))
	touch(t, filepath.Join(cfg.TrainDir, fluxCheckpoint))
	e := startEnv(t, cfg, nil)
	e.assertBases(nil)
}

func TestBases_MossAvailability(t *testing.T) {
	tests := []struct {
		name  string
		files mossFiles
		want  []string
	}{
		{"all files", allMoss, []string{moss}},
		{"vae flux2-vae", mossFiles{checkpoint: true, textEnc: true, vaes: []string{vaeFull}}, []string{moss}},
		{"vae flux2-vae_small", mossFiles{checkpoint: true, textEnc: true, vaes: []string{vaeSmall}}, []string{moss}},
		{"no checkpoint", mossFiles{textEnc: true, vaes: []string{vaeKlein}}, nil},
		{"no text encoder", mossFiles{checkpoint: true, vaes: []string{vaeKlein}}, nil},
		{"no vae", mossFiles{checkpoint: true, textEnc: true}, nil},
		{"other vae name", mossFiles{checkpoint: true, textEnc: true, vaes: []string{aeVAE}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := startEnv(t, mossConfig(t, tt.files), nil)
			e.assertBases(tt.want)
		})
	}
}

func TestBases_SDXLCheckpointChoice(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{"both and photon", []string{sdxlPhoton, sdxlEpic, sdxlJuggernaut}, sdxlJuggernaut},
		{"epic and photon", []string{sdxlPhoton, sdxlEpic}, sdxlEpic},
		{"juggernaut and epic", []string{sdxlEpic, sdxlJuggernaut}, sdxlJuggernaut},
		{"only epic", []string{sdxlEpic}, sdxlEpic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := emptyConfig(t)
			for _, f := range tt.files {
				touch(t, filepath.Join(cfg.ModelsDir, f))
			}
			e := startEnv(t, cfg, nil)
			e.assertBases([]string{sdxl})
			s := e.startJob(baseFields("anna", "woman", sdxl))
			assert.Equal(t, filepath.Join(cfg.ModelsDir, tt.want), onlyValue(t, s.doc, "name_or_path"))
			e.finishJob(s.job.ID())
		})
	}
}

func TestBases_OtherSDXLCheckpointNotEnough(t *testing.T) {
	cfg := emptyConfig(t)
	touch(t, filepath.Join(cfg.ModelsDir, "sd_xl_base_1.0.safetensors"))
	touch(t, filepath.Join(cfg.ModelsDir, "realvisxlV50.safetensors"))
	touch(t, filepath.Join(cfg.ModelsDir, sdxlPhoton))
	e := startEnv(t, cfg, nil)
	e.assertBases(nil)
}

func TestBases_BrowserRequestRejected(t *testing.T) {
	e := startEnv(t, configWith(t, allBases...), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/train/bases", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestBase_DefaultIsZImage(t *testing.T) {
	for _, tt := range []struct {
		name   string
		fields map[string]string
	}{
		{"no base field", personFields("anna", "woman")},
		{"empty base", baseFields("anna", "woman", "")},
		{"explicit Z-Image", baseFields("anna", "woman", zImage)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := startEnv(t, configWith(t, allBases...), nil)
			j, base, doc := e.trainedConfig(tt.fields)
			assert.Equal(t, zImage, base)
			assert.Equal(t, "zimage", onlyValue(t, doc, "arch"))
			assert.Equal(t, zImage, jobBase(t, e.get("/api/train/jobs/"+j.ID())))
			e.finishJob(j.ID())
			people := e.people()
			require.Len(t, people, 1)
			assert.Equal(t, zImage, people[0].Base)
		})
	}
}

func assertRejected(t *testing.T, e *env, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, code, errorCode(t, rec))
	assert.Empty(t, e.jobsOf(testClient))
	calls, _, _ := e.runner.stats()
	assert.Zero(t, calls)
	assert.Empty(t, imagesUnder(t, e.cfg.JobsDir))
	noJobDirs(t, e.cfg.JobsDir)
}

func TestBase_DefaultWithoutZImageFilesRejected(t *testing.T) {
	for _, tt := range []struct {
		name   string
		fields map[string]string
	}{
		{"no base field", personFields("anna", "woman")},
		{"empty base", baseFields("anna", "woman", "")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := startEnv(t, configWith(t, moss, sdxl, flux2), nil)
			rec := e.post(tt.fields, photos(t, 10))
			assertRejected(t, e, rec, "base_unavailable")
		})
	}
}

func TestBase_KnownWithoutFilesRejected(t *testing.T) {
	for _, base := range allBases {
		t.Run(base, func(t *testing.T) {
			e := startEnv(t, configWith(t, without(allBases, base)...), nil)
			require.NotContains(t, e.getBases(), base)
			rec := e.post(baseFields("anna", "woman", base), photos(t, 10))
			assertRejected(t, e, rec, "base_unavailable")
		})
	}
}

func TestBase_KnownRejectedWhenDirsNotConfigured(t *testing.T) {
	for _, base := range allBases {
		t.Run(base, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.ModelsDir, cfg.TextEncoderDir, cfg.VAEDir, cfg.TrainDir = "", "", "", ""
			e := startEnv(t, cfg, nil)
			rec := e.post(baseFields("anna", "woman", base), photos(t, 10))
			assertRejected(t, e, rec, "base_unavailable")
		})
	}
}

func TestMoss_UnavailableRejected(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files mossFiles
	}{
		{"no checkpoint", mossFiles{textEnc: true, vaes: []string{vaeKlein}}},
		{"no text encoder", mossFiles{checkpoint: true, vaes: []string{vaeKlein}}},
		{"no vae", mossFiles{checkpoint: true, textEnc: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := startEnv(t, mossConfig(t, tt.files), nil)
			rec := e.post(baseFields("anna", "woman", moss), photos(t, 10))
			assertRejected(t, e, rec, "base_unavailable")
			assert.NotContains(t, e.getBases(), moss)
		})
	}
}

func TestBase_UnknownRejected(t *testing.T) {
	for _, base := range []string{"sdxl", "moss-image", "z-image", "Moss", "Flux", "flux.2", "Flux.2 Klein 4B", "Chroma", "SD1.5"} {
		t.Run(base, func(t *testing.T) {
			e := startEnv(t, configWith(t, allBases...), nil)
			rec := e.post(baseFields("anna", "woman", base), photos(t, 10))
			assertRejected(t, e, rec, "invalid_request")
		})
	}
}

func assertNoRemoteWeights(t *testing.T, s startedJob) {
	t.Helper()
	assert.NotContains(t, s.raw, "Tongyi-MAI/")
	assert.NotContains(t, s.raw, "ostris/")
	got, ok := onlyValue(t, s.doc, "name_or_path").(string)
	require.True(t, ok)
	assert.True(t, filepath.IsAbs(got), "name_or_path must be absolute: %s", got)
}

func assertJobFiles(t *testing.T, s startedJob) {
	t.Helper()
	assert.Equal(t, filepath.Join(s.spec.WorkDir, "config.yaml"), s.spec.ConfigPath)
	assert.Equal(t, filepath.Join(s.spec.WorkDir, "local.json"), s.spec.LocalPath)
	assert.FileExists(t, s.spec.ConfigPath)
	assert.FileExists(t, s.spec.LocalPath)
}

func TestZImage_ConfigUsesLocalFiles(t *testing.T) {
	cfg := configWith(t, zImage)
	e := startEnv(t, cfg, nil)
	s := e.startJob(baseFields("anna", "woman", zImage))

	assert.Equal(t, zImage, s.base)
	assertJobFiles(t, s)
	assertNoRemoteWeights(t, s)
	assert.Equal(t, "zimage", onlyValue(t, s.doc, "arch"))
	assert.Equal(t, filepath.Join(cfg.TrainDir, zimageCheckpoint), onlyValue(t, s.doc, "name_or_path"))
	assert.Equal(t, filepath.Join(cfg.TrainDir, zimageAdapter), onlyValue(t, s.doc, "assistant_lora_path"))
	assert.Equal(t, map[string]any{
		"te":  filepath.Join(cfg.TextEncoderDir, qwen3TextEnc),
		"vae": filepath.Join(cfg.VAEDir, aeVAE),
	}, s.local)
	e.finishJob(s.job.ID())
}

func TestSDXL_ConfigUsesLocalFiles(t *testing.T) {
	cfg := configWith(t, sdxl)
	e := startEnv(t, cfg, nil)
	s := e.startJob(baseFields("anna", "woman", sdxl))

	assert.Equal(t, sdxl, s.base)
	assertJobFiles(t, s)
	assertNoRemoteWeights(t, s)
	assert.Equal(t, filepath.Join(cfg.ModelsDir, sdxlJuggernaut), onlyValue(t, s.doc, "name_or_path"))
	assert.Equal(t, true, onlyValue(t, s.doc, "is_xl"))
	assert.Equal(t, "ddpm", onlyValue(t, s.doc, "noise_scheduler"))
	assert.Empty(t, valuesOf(s.doc, "arch"))
	res := valuesOf(s.doc, "resolution")
	require.NotEmpty(t, res)
	for _, r := range res {
		assert.Equal(t, []int{1024}, ints(r))
	}
	assert.Equal(t, map[string]any{}, s.local)
	assert.Equal(t, sdxl, jobBase(t, e.get("/api/train/jobs/"+s.job.ID())))
	e.finishJob(s.job.ID())
}

func TestFlux2_ConfigUsesLocalFiles(t *testing.T) {
	cfg := configWith(t, flux2)
	e := startEnv(t, cfg, nil)
	s := e.startJob(baseFields("anna", "woman", flux2))

	assert.Equal(t, flux2, s.base)
	assertJobFiles(t, s)
	assertNoRemoteWeights(t, s)
	assert.Equal(t, "flux2_klein_4b", onlyValue(t, s.doc, "arch"))
	assert.Equal(t, filepath.Join(cfg.ModelsDir, fluxCheckpoint), onlyValue(t, s.doc, "name_or_path"))
	assert.Equal(t, filepath.Join(cfg.VAEDir, vaeKlein), onlyValue(t, s.doc, "vae_path"))
	assert.Empty(t, valuesOf(s.doc, "cache_text_embeddings"))
	assert.Equal(t, map[string]any{"te": filepath.Join(cfg.TextEncoderDir, qwen3TextEnc)}, s.local)
	assert.Equal(t, flux2, jobBase(t, e.get("/api/train/jobs/"+s.job.ID())))
	e.finishJob(s.job.ID())
}

func TestMoss_ConfigUsesLocalFiles(t *testing.T) {
	cfg := mossConfig(t, allMoss)
	e := startEnv(t, cfg, nil)
	s := e.startJob(baseFields("anna", "woman", moss))
	doc := s.doc

	assert.Equal(t, moss, s.base)
	assertJobFiles(t, s)
	assertNoRemoteWeights(t, s)
	assert.Equal(t, "moss_image", onlyValue(t, doc, "arch"))
	assert.Equal(t, filepath.Join(cfg.ModelsDir, mossCheckpoint), onlyValue(t, doc, "name_or_path"))
	assert.Equal(t, filepath.Join(cfg.TextEncoderDir, mossTextEnc), onlyValue(t, doc, "te_name_or_path"))
	assert.Equal(t, filepath.Join(cfg.VAEDir, vaeKlein), onlyValue(t, doc, "vae_path"))
	assert.Equal(t, true, onlyValue(t, doc, "cache_text_embeddings"))
	assert.Equal(t, true, onlyValue(t, doc, "unload_text_encoder"))
	assert.Empty(t, valuesOf(doc, "assistant_lora_path"))
	assert.Empty(t, valuesOf(doc, "caption_dropout_rate"))
	assert.True(t, containsString(doc, "qfloat8"), "moss config must quantize to qfloat8")
	res := valuesOf(doc, "resolution")
	require.NotEmpty(t, res)
	for _, r := range res {
		assert.Equal(t, []int{768}, ints(r))
	}
	assert.Equal(t, map[string]any{}, s.local)
	assert.Equal(t, moss, jobBase(t, e.get("/api/train/jobs/"+s.job.ID())))
	e.finishJob(s.job.ID())
}

func TestBase_RelativeDirsBecomeAbsolute(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	setModelDirs(t, &cfg, root)
	addBaseFiles(t, cfg, allBases...)
	t.Chdir(root)
	cfg.ModelsDir = "checkpoints"
	cfg.TextEncoderDir = "text_encoders"
	cfg.VAEDir = "vae"
	cfg.TrainDir = "train"
	e := startEnv(t, cfg, nil)
	e.assertBases(allBases)

	paths := map[string]map[string]string{
		zImage: {
			"name_or_path":        filepath.Join(root, "train", zimageCheckpoint),
			"assistant_lora_path": filepath.Join(root, "train", zimageAdapter),
		},
		moss: {
			"name_or_path":    filepath.Join(root, "checkpoints", mossCheckpoint),
			"te_name_or_path": filepath.Join(root, "text_encoders", mossTextEnc),
			"vae_path":        filepath.Join(root, "vae", vaeKlein),
		},
		sdxl: {
			"name_or_path": filepath.Join(root, "checkpoints", sdxlJuggernaut),
		},
		flux2: {
			"name_or_path": filepath.Join(root, "checkpoints", fluxCheckpoint),
			"vae_path":     filepath.Join(root, "vae", vaeKlein),
		},
	}
	locals := map[string]map[string]any{
		zImage: {"te": filepath.Join(root, "text_encoders", qwen3TextEnc), "vae": filepath.Join(root, "vae", aeVAE)},
		moss:   {},
		sdxl:   {},
		flux2:  {"te": filepath.Join(root, "text_encoders", qwen3TextEnc)},
	}
	for i, base := range allBases {
		s := e.startJob(baseFields("p"+strings.Repeat("x", i+1), "woman", base))
		for key, want := range paths[base] {
			got, ok := onlyValue(t, s.doc, key).(string)
			require.True(t, ok, "%s %s", base, key)
			assert.True(t, filepath.IsAbs(got), "%s %s must be absolute: %s", base, key, got)
			assert.Equal(t, want, got, "%s %s", base, key)
		}
		assert.Equal(t, locals[base], s.local, base)
		e.finishJob(s.job.ID())
	}
}

func TestMoss_VAEChoiceOrder(t *testing.T) {
	tests := []struct {
		name string
		vaes []string
		want string
	}{
		{"all three", []string{vaeSmall, vaeFull, vaeKlein}, vaeKlein},
		{"full and small", []string{vaeSmall, vaeFull}, vaeFull},
		{"klein and small", []string{vaeSmall, vaeKlein}, vaeKlein},
		{"only small", []string{vaeSmall}, vaeSmall},
		{"only full", []string{vaeFull}, vaeFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mossConfig(t, mossFiles{checkpoint: true, textEnc: true, vaes: tt.vaes})
			e := startEnv(t, cfg, nil)
			j, _, doc := e.trainedConfig(baseFields("anna", "woman", moss))
			assert.Equal(t, filepath.Join(cfg.VAEDir, tt.want), onlyValue(t, doc, "vae_path"))
			e.finishJob(j.ID())
		})
	}
}

func TestMoss_PersonCarriesBase(t *testing.T) {
	cfg := mossConfig(t, allMoss)
	e := startEnv(t, cfg, nil)
	j, _, _ := e.trainedConfig(baseFields("anna", "woman", moss))
	e.finishJob(j.ID())

	data, err := os.ReadFile(filepath.Join(cfg.LoraDir, "anna.person.json"))
	require.NoError(t, err)
	var person map[string]any
	require.NoError(t, json.Unmarshal(data, &person), string(data))
	assert.Equal(t, moss, person["base"])

	people := e.people()
	require.Len(t, people, 1)
	assert.Equal(t, "anna", people[0].Name)
	assert.Equal(t, moss, people[0].Base)

	after := startEnv(t, cfg, nil)
	people = after.people()
	require.Len(t, people, 1)
	assert.Equal(t, moss, people[0].Base)
	assert.Equal(t, moss, jobBase(t, after.get("/api/train/jobs/"+j.ID())))
}

func TestBase_PeopleListMixesBases(t *testing.T) {
	e := startEnv(t, configWith(t, allBases...), nil)
	names := map[string]string{zImage: "anna", moss: "boris", sdxl: "clara", flux2: "dmitry"}
	for _, base := range allBases {
		j, _, _ := e.trainedConfig(baseFields(names[base], "person", base))
		e.finishJob(j.ID())
	}

	got := map[string]string{}
	for _, p := range e.people() {
		got[p.Name] = p.Base
	}
	assert.Equal(t, map[string]string{"anna": zImage, "boris": moss, "clara": sdxl, "dmitry": flux2}, got)
}

func rewriteJSON(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc), string(data))
	edit(doc)
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o644))
}

func TestBase_OldFilesWithoutBaseAreZImage(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"base missing", func(d map[string]any) { delete(d, "base") }},
		{"base empty", func(d map[string]any) { d["base"] = "" }},
		{"base Z-Image", func(d map[string]any) { d["base"] = zImage }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := configWith(t, allBases...)
			before := startEnv(t, cfg, nil)
			j := before.submit("anna", "woman")
			before.waitStarted()
			before.finishJob(j.ID())

			rewriteJSON(t, filepath.Join(cfg.LoraDir, "anna.person.json"), tt.edit)
			rewriteJSON(t, filepath.Join(cfg.JobsDir, j.ID(), "status.json"), tt.edit)

			after := startEnv(t, cfg, nil)
			people := after.people()
			require.Len(t, people, 1)
			assert.Equal(t, personView{Name: "anna", Trigger: "anna_woman", Gender: "woman", Base: zImage, Lora: "anna", Steps: 10}, people[0])
			rec := after.get("/api/train/jobs/" + j.ID())
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, zImage, jobBase(t, rec))
			assert.Equal(t, "completed", decodeJob(t, rec).Status)
		})
	}
}

func TestBase_FilesGoneBeforeStartFailsJob(t *testing.T) {
	tests := []struct {
		base   string
		first  string
		remove func(cfg Config) string
	}{
		{zImage, sdxl, func(cfg Config) string { return filepath.Join(cfg.TrainDir, zimageCheckpoint) }},
		{zImage + " adapter", sdxl, func(cfg Config) string { return filepath.Join(cfg.TrainDir, zimageAdapter) }},
		{zImage + " vae", sdxl, func(cfg Config) string { return filepath.Join(cfg.VAEDir, aeVAE) }},
		{moss, zImage, func(cfg Config) string { return filepath.Join(cfg.ModelsDir, mossCheckpoint) }},
		{sdxl, zImage, func(cfg Config) string { return filepath.Join(cfg.ModelsDir, sdxlJuggernaut) }},
		{flux2, zImage, func(cfg Config) string { return filepath.Join(cfg.ModelsDir, fluxCheckpoint) }},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			base := strings.Fields(tt.base)[0]
			cfg := configWith(t, allBases...)
			e := startEnv(t, cfg, nil)
			rec := e.post(baseFields("zara", "woman", tt.first), photos(t, 10))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			first := decodeJob(t, rec)
			e.waitStarted()
			rec = e.post(baseFields("anna", "woman", base), photos(t, 10))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			queued := decodeJob(t, rec)
			require.NoError(t, os.Remove(tt.remove(cfg)))
			e.finishRun(runResult{writeOutput: true})
			e.waitStatus(first.ID(), "completed")
			j := e.waitStatus(queued.ID(), "failed")
			require.NotNil(t, j.Error)
			assert.Equal(t, "base_unavailable", j.Error.Code)
			e.m.Wait()
			calls, _, _ := e.runner.stats()
			assert.Equal(t, 1, calls)
			ppl := e.people()
			require.Len(t, ppl, 1)
			assert.Equal(t, "zara", ppl[0].Name)
		})
	}
}
