package train

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modeFields(name, gender, mode string) map[string]string {
	f := personFields(name, gender)
	if mode != "" {
		f["mode"] = mode
	}
	return f
}

func newPhotos(t *testing.T, n int) []upload {
	t.Helper()
	out := make([]upload, 0, n)
	for i := 0; i < n; i++ {
		seed := 150 + i
		out = append(out, upload{filename: fmt.Sprintf("n%02d.png", i), contentType: "image/png", data: pngBytes(t, seed)})
	}
	return out
}

func (e *env) postWithNew(fields map[string]string, plain, fresh []upload) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(e.t, w.WriteField(k, v))
	}
	write := func(field string, files []upload) {
		for _, f := range files {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, f.filename))
			h.Set("Content-Type", f.contentType)
			p, err := w.CreatePart(h)
			require.NoError(e.t, err)
			_, err = p.Write(f.data)
			require.NoError(e.t, err)
		}
	}
	write("photos", plain)
	write("new_photos", fresh)
	require.NoError(e.t, w.Close())
	return e.do(http.MethodPost, "/api/train/jobs", &buf, w.FormDataContentType())
}

func rawJob(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw), rec.Body.String())
	return raw
}

func num(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	v, ok := m[key]
	require.True(t, ok, "field %s missing in %v", key, m)
	f, ok := v.(float64)
	require.True(t, ok, "field %s is not a number: %v", key, v)
	return int(f)
}

func (e *env) card(name string) map[string]any {
	e.t.Helper()
	return readJSONFile(e.t, filepath.Join(e.cfg.LoraDir, name+".person.json"))
}

func (e *env) setCard(name string, edit func(map[string]any)) {
	e.t.Helper()
	rewriteJSON(e.t, filepath.Join(e.cfg.LoraDir, name+".person.json"), edit)
}

func (e *env) loraContent(name string) string {
	e.t.Helper()
	data, err := os.ReadFile(e.loraPath(name))
	require.NoError(e.t, err)
	return string(data)
}

func (e *env) rollback(name string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/train/people/"+name+"/rollback", nil, "")
}

func (e *env) previousOf(name string) bool {
	e.t.Helper()
	p, ok := e.peopleRaw()[name]
	require.True(e.t, ok, "person %s not listed", name)
	v, _ := p["previous"].(bool)
	return v
}

func (e *env) safetensorsInLoraDir() []string {
	e.t.Helper()
	var out []string
	err := filepath.WalkDir(e.cfg.LoraDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".safetensors") {
			rel, rerr := filepath.Rel(e.cfg.LoraDir, path)
			require.NoError(e.t, rerr)
			out = append(out, rel)
		}
		return nil
	})
	require.NoError(e.t, err)
	return out
}

func (e *env) trainMode(fields map[string]string) (map[string]any, runCall) {
	e.t.Helper()
	rec := e.post(fields, photos(e.t, 10))
	require.Equal(e.t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := rawJob(e.t, rec)
	call := e.waitStarted()
	e.finishJob(decodeJob(e.t, rec).ID())
	return j, call
}

func TestModes_StepsByMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		existing bool
		wantMode string
		steps    func(cfg Config) int
	}{
		{"empty is full", "", false, "full", func(cfg Config) int { return cfg.Steps }},
		{"full", "full", false, "full", func(cfg Config) int { return cfg.Steps }},
		{"quick", "quick", false, "quick", func(Config) int { return 600 }},
		{"continue", "continue", true, "continue", func(Config) int { return 400 }},
		{"retrain", "retrain", true, "retrain", func(cfg Config) int { return cfg.Steps }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.existing {
				writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
			}
			want := tt.steps(e.cfg)

			rec := e.post(modeFields("anna", "woman", tt.mode), photos(t, 10))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			raw := rawJob(t, rec)
			assert.Equal(t, tt.wantMode, raw["mode"])
			assert.Equal(t, want, num(t, raw, "steps"))
			j := decodeJob(t, rec)
			assert.Equal(t, want, j.Progress.Steps)

			call := e.waitStarted()
			assert.Equal(t, want, call.spec.Steps)

			got := rawJob(t, e.get("/api/train/jobs/"+j.ID()))
			assert.Equal(t, tt.wantMode, got["mode"])
			assert.Equal(t, want, num(t, got, "steps"))
			assert.Equal(t, want, e.job(j.ID()).Progress.Steps)
		})
	}
}

func TestModes_DefaultStepsWhenConfigZero(t *testing.T) {
	cfg := testConfig(t)
	cfg.Steps = 0
	e := startEnv(t, cfg, nil)

	rec := e.post(modeFields("anna", "woman", "full"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, 2000, num(t, rawJob(t, rec), "steps"))
	assert.Equal(t, 2000, e.waitStarted().spec.Steps)
}

func TestModes_UnknownModeRejected(t *testing.T) {
	for _, mode := range []string{"turbo", "resume", "fast"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

			rec := e.post(modeFields("bella", "woman", mode), photos(t, 10))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_request", errorCode(t, rec))
			assert.Empty(t, e.jobsOf(testClient))
			calls, _, _ := e.runner.stats()
			assert.Zero(t, calls)
		})
	}
}

func TestModes_ContinueRetrainNeedExistingPerson(t *testing.T) {
	setups := []struct {
		name  string
		setup func(t *testing.T, e *env)
	}{
		{"nothing", func(t *testing.T, e *env) {}},
		{"lora only", func(t *testing.T, e *env) {
			require.NoError(t, os.WriteFile(e.loraPath("anna"), []byte("lora-anna"), 0o644))
		}},
		{"card only", func(t *testing.T, e *env) {
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
			require.NoError(t, os.Remove(e.loraPath("anna")))
		}},
	}
	for _, mode := range []string{"continue", "retrain"} {
		for _, s := range setups {
			t.Run(mode+"/"+s.name, func(t *testing.T) {
				e := newEnv(t)
				s.setup(t, e)

				rec := e.post(modeFields("anna", "woman", mode), photos(t, 10))
				require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
				assert.Equal(t, "not_found", errorCode(t, rec))
				assert.Empty(t, e.jobsOf(testClient))
			})
		}
	}
}

func TestModes_ContinueRetrainTakeCardValues(t *testing.T) {
	for _, mode := range []string{"continue", "retrain"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "anna", baseZImage, "Аня")

			fields := modeFields("anna", "man", mode)
			fields["base"] = baseSDXL
			fields["display_name"] = "Другая"
			rec := e.post(fields, photos(t, 10))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			j := decodeJob(t, rec)
			assert.Equal(t, "woman", j.Gender)
			assert.Equal(t, "anna_woman", j.Trigger)
			assert.Equal(t, baseZImage, jobBase(t, rec))

			e.waitStarted()
			e.finishJob(j.ID())

			card := e.card("anna")
			assert.Equal(t, "woman", card["gender"])
			assert.Equal(t, baseZImage, card["base"])
			assert.Equal(t, "Аня", card["display_name"])
			got, ok := e.displayOf("anna")
			assert.True(t, ok)
			assert.Equal(t, "Аня", got)
		})
	}
}

func TestModes_ContinueRetrainBusy(t *testing.T) {
	for _, mode := range []string{"continue", "retrain"} {
		t.Run(mode+"/running", func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
			first := e.post(modeFields("anna", "woman", "continue"), photos(t, 10))
			require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
			e.waitStarted()

			fields := modeFields("anna", "woman", mode)
			fields["client"] = "other"
			rec := e.post(fields, photos(t, 10))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "busy", errorCode(t, rec))
			assert.Empty(t, e.jobsOf("other"))
		})
		t.Run(mode+"/queued", func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
			e.submit("boris", "man")
			e.waitStarted()
			q := e.post(modeFields("anna", "woman", "retrain"), photos(t, 10))
			require.Equal(t, http.StatusAccepted, q.Code, q.Body.String())
			e.waitStatus(decodeJob(t, q).ID(), "queued")

			fields := modeFields("anna", "woman", mode)
			fields["client"] = "other"
			rec := e.post(fields, photos(t, 10))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "busy", errorCode(t, rec))
			assert.Empty(t, e.jobsOf("other"))
		})
		t.Run(mode+"/paused", func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
			first := e.post(modeFields("anna", "woman", "continue"), photos(t, 10))
			require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
			e.waitStarted()
			e.pauseRunning(decodeJob(t, first).ID())

			fields := modeFields("anna", "woman", mode)
			fields["client"] = "other"
			rec := e.post(fields, photos(t, 10))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "busy", errorCode(t, rec))
			assert.Empty(t, e.jobsOf("other"))
		})
	}
}

func TestModes_FullQuickExistingNameTaken(t *testing.T) {
	for _, mode := range []string{"", "full", "quick"} {
		t.Run("m"+mode, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

			rec := e.post(modeFields("anna", "woman", mode), photos(t, 10))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "name_taken", errorCode(t, rec))
			assert.Equal(t, "lora-anna", e.loraContent("anna"))
		})
	}
}

func TestModes_NewPhotosOnlyWithContinue(t *testing.T) {
	for _, mode := range []string{"", "full", "quick", "retrain"} {
		t.Run("m"+mode, func(t *testing.T) {
			e := newEnv(t)
			name := "bella"
			if mode == "retrain" {
				writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
				name = "anna"
			}
			rec := e.postWithNew(modeFields(name, "woman", mode), photos(t, 10), newPhotos(t, 2))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_request", errorCode(t, rec))
			assert.Empty(t, e.jobsOf(testClient))
		})
	}
}

func TestModes_ContinuePhotoCounts(t *testing.T) {
	tests := []struct {
		name   string
		plain  int
		fresh  int
		accept bool
	}{
		{"10 plain", 10, 0, true},
		{"8 plain 2 new", 8, 2, true},
		{"9 total", 6, 3, false},
		{"100 total", 90, 10, true},
		{"101 total", 90, 11, false},
		{"only new 10", 0, 10, true},
		{"only new 9", 0, 9, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

			rec := e.postWithNew(modeFields("anna", "woman", "continue"), photos(t, tt.plain), newPhotos(t, tt.fresh))
			if !tt.accept {
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				assert.Equal(t, "invalid_request", errorCode(t, rec))
				assert.Empty(t, e.jobsOf(testClient))
				return
			}
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			raw := rawJob(t, rec)
			assert.Equal(t, tt.fresh, num(t, raw, "new_photos"))
			assert.Equal(t, tt.plain+tt.fresh, num(t, raw, "photos"))
		})
	}
}

func datasetsOf(t *testing.T, doc any) []map[string]any {
	t.Helper()
	vals := valuesOf(doc, "datasets")
	require.Len(t, vals, 1, "datasets must appear once")
	list, ok := vals[0].([]any)
	require.True(t, ok, "datasets must be a list: %v", vals[0])
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		m, ok := d.(map[string]any)
		require.True(t, ok)
		out = append(out, m)
	}
	return out
}

func TestModes_NewPhotosRepeatedInConfig(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

	rec := e.postWithNew(modeFields("anna", "woman", "continue"), photos(t, 10), newPhotos(t, 3))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	call := e.waitStarted()
	assert.Equal(t, filepath.Join(e.cfg.JobsDir, decodeJob(t, rec).ID()), filepath.Dir(call.spec.ConfigPath))
	doc := readYAML(t, call.spec.ConfigPath)

	ds := datasetsOf(t, doc)
	require.Len(t, ds, 2)
	var repeated, plain []map[string]any
	for _, d := range ds {
		if _, ok := d["num_repeats"]; ok {
			repeated = append(repeated, d)
		} else {
			plain = append(plain, d)
		}
	}
	require.Len(t, repeated, 1, "exactly one dataset repeats: %v", ds)
	assert.Equal(t, 3, repeated[0]["num_repeats"])
	newDir, _ := repeated[0]["folder_path"].(string)
	require.NotEmpty(t, newDir)
	assert.Equal(t, 3, countImages(newDir))
	require.Len(t, plain, 1)
	plainDir, _ := plain[0]["folder_path"].(string)
	require.NotEmpty(t, plainDir)
	assert.Equal(t, 10, countImages(plainDir))
	e.finishJob(decodeJob(t, rec).ID())
}

func TestModes_SingleDatasetWithoutNewPhotos(t *testing.T) {
	tests := []struct {
		name  string
		plain int
		fresh int
	}{
		{"only plain", 10, 0},
		{"only new", 0, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

			rec := e.postWithNew(modeFields("anna", "woman", "continue"), photos(t, tt.plain), newPhotos(t, tt.fresh))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			call := e.waitStarted()
			doc := readYAML(t, call.spec.ConfigPath)
			ds := datasetsOf(t, doc)
			require.Len(t, ds, 1)
			assert.Empty(t, valuesOf(doc, "num_repeats"))
			dir, _ := ds[0]["folder_path"].(string)
			assert.Equal(t, 10, countImages(dir))
			e.finishJob(decodeJob(t, rec).ID())
		})
	}
}

func TestModes_PretrainedLoraOnlyForContinue(t *testing.T) {
	tests := []struct {
		mode     string
		existing bool
		want     bool
	}{
		{"continue", true, true},
		{"retrain", true, false},
		{"full", false, false},
		{"quick", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		t.Run("m"+tt.mode, func(t *testing.T) {
			e := newEnv(t)
			if tt.existing {
				writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
			}
			s := e.startJob(modeFields("anna", "woman", tt.mode))
			if tt.want {
				assert.Equal(t, e.loraPath("anna"), onlyValue(t, s.doc, "pretrained_lora_path"))
				assert.Contains(t, s.raw, "pretrained_lora_path:")
			} else {
				assert.Empty(t, valuesOf(s.doc, "pretrained_lora_path"))
				assert.NotContains(t, s.raw, "pretrained_lora_path")
			}
			e.finishJob(s.job.ID())
		})
	}
}

func parseTime(t *testing.T, v any) time.Time {
	t.Helper()
	s, ok := v.(string)
	require.True(t, ok, "time is not a string: %v", v)
	tm, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return tm
}

func TestModes_CardAfterNewPerson(t *testing.T) {
	tests := []struct {
		mode     string
		wantMode string
		steps    func(cfg Config) int
	}{
		{"", "full", func(cfg Config) int { return cfg.Steps }},
		{"full", "full", func(cfg Config) int { return cfg.Steps }},
		{"quick", "quick", func(Config) int { return 600 }},
	}
	for _, tt := range tests {
		t.Run("m"+tt.mode, func(t *testing.T) {
			e := newEnv(t)
			j, _ := e.trainMode(modeFields("anna", "woman", tt.mode))

			card := e.card("anna")
			assert.Equal(t, tt.wantMode, card["mode"])
			assert.Equal(t, 0, num(t, card, "rounds"))
			assert.Equal(t, tt.steps(e.cfg), num(t, card, "steps"))
			assert.True(t, parseTime(t, j["created_at"]).Equal(parseTime(t, card["photos_at"])))
			assert.False(t, e.previousOf("anna"))
		})
	}
}

func TestModes_ContinueCard(t *testing.T) {
	tests := []struct {
		name     string
		oldMode  any
		rounds   int
		steps    int
		wantMode string
	}{
		{"quick kept", "quick", 0, 600, "quick"},
		{"full kept", "full", 2, 2000, "full"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "anna", baseZImage, "Аня")
			e.setCard("anna", func(c map[string]any) {
				c["mode"] = tt.oldMode
				c["rounds"] = tt.rounds
				c["steps"] = tt.steps
			})

			j, _ := e.trainMode(modeFields("anna", "woman", "continue"))

			card := e.card("anna")
			assert.Equal(t, tt.wantMode, card["mode"])
			assert.Equal(t, tt.rounds+1, num(t, card, "rounds"))
			assert.Equal(t, tt.steps+400, num(t, card, "steps"))
			assert.Equal(t, "Аня", card["display_name"])
			assert.True(t, parseTime(t, j["created_at"]).Equal(parseTime(t, card["photos_at"])))
			assert.Equal(t, "lora-weights", e.loraContent("anna"))
			assert.True(t, e.previousOf("anna"))
		})
	}
}

func TestModes_RetrainCard(t *testing.T) {
	e := newEnv(t)
	writePersonDisplay(t, e.cfg.LoraDir, "anna", baseZImage, "Аня")
	e.setCard("anna", func(c map[string]any) {
		c["mode"] = "quick"
		c["rounds"] = 3
		c["steps"] = 1800
	})

	j, _ := e.trainMode(modeFields("anna", "woman", "retrain"))

	card := e.card("anna")
	assert.Equal(t, "full", card["mode"])
	assert.Equal(t, 0, num(t, card, "rounds"))
	assert.Equal(t, e.cfg.Steps, num(t, card, "steps"))
	assert.Equal(t, "Аня", card["display_name"])
	assert.True(t, parseTime(t, j["created_at"]).Equal(parseTime(t, card["photos_at"])))
	assert.Equal(t, "lora-weights", e.loraContent("anna"))
	assert.True(t, e.previousOf("anna"))
}

func TestModes_FailedContinueKeepsCurrent(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	before := e.card("anna")

	rec := e.post(modeFields("anna", "woman", "continue"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := decodeJob(t, rec)
	e.waitStarted()
	e.finishRun(runResult{err: fmt.Errorf("boom")})
	e.waitStatus(j.ID(), "failed")
	e.m.Wait()

	assert.Equal(t, "lora-anna", e.loraContent("anna"))
	assert.Equal(t, before, e.card("anna"))
	assert.False(t, e.previousOf("anna"))
}

func TestModes_PreviousNotInLoraDir(t *testing.T) {
	for _, mode := range []string{"continue", "retrain"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

			e.trainMode(modeFields("anna", "woman", mode))
			assert.Equal(t, []string{"anna.safetensors"}, e.safetensorsInLoraDir())

			rec := e.rollback("anna")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, []string{"anna.safetensors"}, e.safetensorsInLoraDir())
		})
	}
}

func TestRollback_SwapsVersions(t *testing.T) {
	e := newEnv(t)
	writePersonDisplay(t, e.cfg.LoraDir, "anna", baseZImage, "Аня")
	e.setCard("anna", func(c map[string]any) {
		c["mode"] = "quick"
		c["rounds"] = 1
		c["steps"] = 600
	})
	oldCard := e.card("anna")

	e.trainMode(modeFields("anna", "woman", "continue"))
	newCard := e.card("anna")
	require.Equal(t, "lora-weights", e.loraContent("anna"))

	rec := e.rollback("anna")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "lora-anna", e.loraContent("anna"))
	assert.Equal(t, 600, num(t, e.card("anna"), "steps"))
	assert.Equal(t, 1, num(t, e.card("anna"), "rounds"))
	assert.Equal(t, oldCard["mode"], e.card("anna")["mode"])
	assert.Equal(t, "Аня", e.card("anna")["display_name"])
	assert.True(t, e.previousOf("anna"))
	assert.Equal(t, 600, num(t, e.peopleRaw()["anna"], "steps"))

	rec = e.rollback("anna")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "lora-weights", e.loraContent("anna"))
	assert.Equal(t, num(t, newCard, "steps"), num(t, e.card("anna"), "steps"))
	assert.Equal(t, num(t, newCard, "rounds"), num(t, e.card("anna"), "rounds"))
	assert.True(t, e.previousOf("anna"))
}

func TestRollback_Errors(t *testing.T) {
	t.Run("no previous", func(t *testing.T) {
		e := newEnv(t)
		writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
		rec := e.rollback("anna")
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "no_previous", errorCode(t, rec))
		assert.Equal(t, "lora-anna", e.loraContent("anna"))
	})
	t.Run("trained once has no previous", func(t *testing.T) {
		e := newEnv(t)
		e.trainMode(modeFields("anna", "woman", "full"))
		rec := e.rollback("anna")
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "no_previous", errorCode(t, rec))
	})
	t.Run("unknown person", func(t *testing.T) {
		e := newEnv(t)
		rec := e.rollback("nobody")
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Equal(t, "not_found", errorCode(t, rec))
	})
	t.Run("busy", func(t *testing.T) {
		e := newEnv(t)
		writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
		e.trainMode(modeFields("anna", "woman", "continue"))
		rec := e.post(modeFields("anna", "woman", "continue"), photos(t, 10))
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		e.waitStarted()

		rb := e.rollback("anna")
		require.Equal(t, http.StatusConflict, rb.Code, rb.Body.String())
		assert.Equal(t, "busy", errorCode(t, rb))
		assert.Equal(t, "lora-weights", e.loraContent("anna"))
	})
}

func TestModes_DeleteRemovesPrevious(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	e.trainMode(modeFields("anna", "woman", "continue"))
	require.True(t, e.previousOf("anna"))

	rec := e.do(http.MethodDelete, "/api/train/people/anna", nil, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NoFileExists(t, e.loraPath("anna"))

	root := filepath.Dir(e.cfg.LoraDir)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr == nil {
			assert.NotEqual(t, "lora-anna", string(data), "previous LoRA left at %s", path)
		}
		return nil
	})
	require.NoError(t, err)

	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	assert.False(t, e.previousOf("anna"))
	rb := e.rollback("anna")
	require.Equal(t, http.StatusConflict, rb.Code, rb.Body.String())
	assert.Equal(t, "no_previous", errorCode(t, rb))
}

func TestModes_RenameMovesPrevious(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	e.trainMode(modeFields("anna", "woman", "continue"))

	rec := e.rename("anna", `{"name":"maria"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, e.previousOf("maria"))
	assert.Equal(t, []string{"maria.safetensors"}, e.safetensorsInLoraDir())

	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
	assert.False(t, e.previousOf("anna"))
	rb := e.rollback("anna")
	require.Equal(t, http.StatusConflict, rb.Code, rb.Body.String())
	assert.Equal(t, "no_previous", errorCode(t, rb))

	rb = e.rollback("maria")
	require.Equal(t, http.StatusOK, rb.Code, rb.Body.String())
	assert.Equal(t, "lora-anna", e.loraContent("maria"))
	assert.True(t, e.previousOf("maria"))
}

func TestModes_RenameBusy(t *testing.T) {
	for _, mode := range []string{"continue", "retrain"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)
			rec := e.post(modeFields("anna", "woman", mode), photos(t, 10))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			e.waitStarted()

			rn := e.rename("anna", `{"name":"maria"}`)
			require.Equal(t, http.StatusConflict, rn.Code, rn.Body.String())
			assert.Equal(t, "busy", errorCode(t, rn))
			assert.FileExists(t, e.loraPath("anna"))
			assert.NoFileExists(t, e.loraPath("maria"))
		})
	}
}
