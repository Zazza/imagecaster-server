package train

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	mossCheckpoint = "moss_image_sft_bf16.safetensors"
	mossTextEnc    = "qwen35_9b_int8_convrot.safetensors"
	vaeKlein       = "flux2-klein-vae.safetensors"
	vaeFull        = "flux2-vae.safetensors"
	vaeSmall       = "flux2-vae_small.safetensors"
)

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
	cfg := testConfig(t)
	root := t.TempDir()
	cfg.ModelsDir = filepath.Join(root, "checkpoints")
	cfg.TextEncoderDir = filepath.Join(root, "text_encoders")
	cfg.VAEDir = filepath.Join(root, "vae")
	for _, d := range []string{cfg.ModelsDir, cfg.TextEncoderDir, cfg.VAEDir} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
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

func (e *env) trainedConfig(fields map[string]string) (jobView, string, any) {
	e.t.Helper()
	rec := e.post(fields, photos(e.t, 10))
	require.Equal(e.t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := decodeJob(e.t, rec)
	call := e.waitStarted()
	return j, jobBase(e.t, rec), readYAML(e.t, call.spec.ConfigPath)
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

func TestBases_Availability(t *testing.T) {
	tests := []struct {
		name  string
		files mossFiles
		want  []string
	}{
		{"all files", allMoss, []string{"Z-Image", "Moss-Image"}},
		{"vae flux2-vae", mossFiles{checkpoint: true, textEnc: true, vaes: []string{vaeFull}}, []string{"Z-Image", "Moss-Image"}},
		{"vae flux2-vae_small", mossFiles{checkpoint: true, textEnc: true, vaes: []string{vaeSmall}}, []string{"Z-Image", "Moss-Image"}},
		{"no checkpoint", mossFiles{textEnc: true, vaes: []string{vaeKlein}}, []string{"Z-Image"}},
		{"no text encoder", mossFiles{checkpoint: true, vaes: []string{vaeKlein}}, []string{"Z-Image"}},
		{"no vae", mossFiles{checkpoint: true, textEnc: true}, []string{"Z-Image"}},
		{"other vae name", mossFiles{checkpoint: true, textEnc: true, vaes: []string{"ae.safetensors"}}, []string{"Z-Image"}},
		{"nothing", mossFiles{}, []string{"Z-Image"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := startEnv(t, mossConfig(t, tt.files), nil)
			assert.Equal(t, tt.want, e.m.Bases())
			assert.Equal(t, tt.want, e.getBases())
		})
	}
}

func TestBases_DirsNotConfigured(t *testing.T) {
	e := newEnv(t)
	assert.Equal(t, []string{"Z-Image"}, e.m.Bases())
	assert.Equal(t, []string{"Z-Image"}, e.getBases())
}

func TestBases_FilesInWrongDirs(t *testing.T) {
	cfg := mossConfig(t, mossFiles{})
	touch(t, filepath.Join(cfg.TextEncoderDir, mossCheckpoint))
	touch(t, filepath.Join(cfg.VAEDir, mossTextEnc))
	touch(t, filepath.Join(cfg.ModelsDir, vaeKlein))
	e := startEnv(t, cfg, nil)
	assert.Equal(t, []string{"Z-Image"}, e.m.Bases())
}

func TestBases_BrowserRequestRejected(t *testing.T) {
	e := startEnv(t, mossConfig(t, allMoss), nil)
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
		{"explicit Z-Image", baseFields("anna", "woman", "Z-Image")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := startEnv(t, mossConfig(t, allMoss), nil)
			j, base, doc := e.trainedConfig(tt.fields)
			assert.Equal(t, "Z-Image", base)
			assert.Equal(t, "zimage", onlyValue(t, doc, "arch"))
			assert.Equal(t, "Z-Image", jobBase(t, e.get("/api/train/jobs/"+j.ID())))
			e.finishJob(j.ID())
			people := e.people()
			require.Len(t, people, 1)
			assert.Equal(t, "Z-Image", people[0].Base)
		})
	}
}

func TestMoss_ConfigUsesLocalFiles(t *testing.T) {
	cfg := mossConfig(t, allMoss)
	e := startEnv(t, cfg, nil)
	j, base, doc := e.trainedConfig(baseFields("anna", "woman", "Moss-Image"))

	assert.Equal(t, "Moss-Image", base)
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
	assert.Equal(t, "Moss-Image", jobBase(t, e.get("/api/train/jobs/"+j.ID())))
	e.finishJob(j.ID())
}

func TestMoss_RelativeDirsBecomeAbsolute(t *testing.T) {
	cfg := mossConfig(t, allMoss)
	root := filepath.Dir(cfg.ModelsDir)
	t.Chdir(root)
	cfg.ModelsDir = "checkpoints"
	cfg.TextEncoderDir = "text_encoders"
	cfg.VAEDir = "vae"
	e := startEnv(t, cfg, nil)
	j, _, doc := e.trainedConfig(baseFields("anna", "woman", "Moss-Image"))
	for key, want := range map[string]string{
		"name_or_path":    filepath.Join(root, "checkpoints", mossCheckpoint),
		"te_name_or_path": filepath.Join(root, "text_encoders", mossTextEnc),
		"vae_path":        filepath.Join(root, "vae", vaeKlein),
	} {
		got, ok := onlyValue(t, doc, key).(string)
		require.True(t, ok, key)
		assert.True(t, filepath.IsAbs(got), "%s must be absolute: %s", key, got)
		assert.Equal(t, want, got, key)
	}
	e.finishJob(j.ID())
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
			j, _, doc := e.trainedConfig(baseFields("anna", "woman", "Moss-Image"))
			assert.Equal(t, filepath.Join(cfg.VAEDir, tt.want), onlyValue(t, doc, "vae_path"))
			e.finishJob(j.ID())
		})
	}
}

func TestMoss_UnavailableRejected(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  func(t *testing.T) Config
	}{
		{"dirs not configured", testConfig},
		{"no checkpoint", func(t *testing.T) Config {
			return mossConfig(t, mossFiles{textEnc: true, vaes: []string{vaeKlein}})
		}},
		{"no text encoder", func(t *testing.T) Config {
			return mossConfig(t, mossFiles{checkpoint: true, vaes: []string{vaeKlein}})
		}},
		{"no vae", func(t *testing.T) Config {
			return mossConfig(t, mossFiles{checkpoint: true, textEnc: true})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg(t)
			e := startEnv(t, cfg, nil)
			rec := e.post(baseFields("anna", "woman", "Moss-Image"), photos(t, 10))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "base_unavailable", errorCode(t, rec))
			assert.NotContains(t, e.getBases(), "Moss-Image")
			assert.Empty(t, e.jobsOf(testClient))
			calls, _, _ := e.runner.stats()
			assert.Zero(t, calls)
			assert.Empty(t, imagesUnder(t, cfg.JobsDir))
			noJobDirs(t, cfg.JobsDir)
		})
	}
}

func TestBase_UnknownRejected(t *testing.T) {
	for _, base := range []string{"SDXL", "moss-image", "z-image", "Moss", "Flux"} {
		t.Run(base, func(t *testing.T) {
			cfg := mossConfig(t, allMoss)
			e := startEnv(t, cfg, nil)
			rec := e.post(baseFields("anna", "woman", base), photos(t, 10))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_request", errorCode(t, rec))
			assert.Empty(t, e.jobsOf(testClient))
			calls, _, _ := e.runner.stats()
			assert.Zero(t, calls)
			assert.Empty(t, imagesUnder(t, cfg.JobsDir))
			noJobDirs(t, cfg.JobsDir)
		})
	}
}

func TestMoss_PersonCarriesBase(t *testing.T) {
	cfg := mossConfig(t, allMoss)
	e := startEnv(t, cfg, nil)
	j, _, _ := e.trainedConfig(baseFields("anna", "woman", "Moss-Image"))
	e.finishJob(j.ID())

	data, err := os.ReadFile(filepath.Join(cfg.LoraDir, "anna.person.json"))
	require.NoError(t, err)
	var person map[string]any
	require.NoError(t, json.Unmarshal(data, &person), string(data))
	assert.Equal(t, "Moss-Image", person["base"])

	people := e.people()
	require.Len(t, people, 1)
	assert.Equal(t, "anna", people[0].Name)
	assert.Equal(t, "Moss-Image", people[0].Base)

	after := startEnv(t, cfg, nil)
	people = after.people()
	require.Len(t, people, 1)
	assert.Equal(t, "Moss-Image", people[0].Base)
	assert.Equal(t, "Moss-Image", jobBase(t, after.get("/api/train/jobs/"+j.ID())))
}

func TestBase_PeopleListMixesBases(t *testing.T) {
	e := startEnv(t, mossConfig(t, allMoss), nil)
	z, _, _ := e.trainedConfig(baseFields("anna", "woman", "Z-Image"))
	e.finishJob(z.ID())
	m, _, _ := e.trainedConfig(baseFields("boris", "man", "Moss-Image"))
	e.finishJob(m.ID())

	got := map[string]string{}
	for _, p := range e.people() {
		got[p.Name] = p.Base
	}
	assert.Equal(t, map[string]string{"anna": "Z-Image", "boris": "Moss-Image"}, got)
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
		{"base Z-Image", func(d map[string]any) { d["base"] = "Z-Image" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mossConfig(t, allMoss)
			before := startEnv(t, cfg, nil)
			j := before.submit("anna", "woman")
			before.waitStarted()
			before.finishJob(j.ID())

			rewriteJSON(t, filepath.Join(cfg.LoraDir, "anna.person.json"), tt.edit)
			rewriteJSON(t, filepath.Join(cfg.JobsDir, j.ID(), "status.json"), tt.edit)

			after := startEnv(t, cfg, nil)
			people := after.people()
			require.Len(t, people, 1)
			assert.Equal(t, personView{Name: "anna", Trigger: "anna_woman", Gender: "woman", Base: "Z-Image", Lora: "anna", Steps: 10}, people[0])
			rec := after.get("/api/train/jobs/" + j.ID())
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "Z-Image", jobBase(t, rec))
			assert.Equal(t, "completed", decodeJob(t, rec).Status)
		})
	}
}

func TestMoss_FilesGoneBeforeStartFailsJob(t *testing.T) {
	cfg := mossConfig(t, allMoss)
	e := startEnv(t, cfg, nil)
	first := e.submit("zara", "woman")
	e.waitStarted()
	rec := e.post(baseFields("anna", "woman", "Moss-Image"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	moss := decodeJob(t, rec)
	require.NoError(t, os.Remove(filepath.Join(cfg.ModelsDir, mossCheckpoint)))
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(first.ID(), "completed")
	j := e.waitStatus(moss.ID(), "failed")
	require.NotNil(t, j.Error)
	assert.Equal(t, "base_unavailable", j.Error.Code)
	e.m.Wait()
	calls, _, _ := e.runner.stats()
	assert.Equal(t, 1, calls)
	ppl := e.people()
	require.Len(t, ppl, 1)
	assert.Equal(t, "zara", ppl[0].Name)
}
