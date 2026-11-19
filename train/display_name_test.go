package train

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (e *env) setDisplay(name, display string, replace bool) *httptest.ResponseRecorder {
	e.t.Helper()
	body, err := json.Marshal(map[string]any{"display_name": display, "replace": replace})
	require.NoError(e.t, err)
	return e.do(http.MethodPost, "/api/train/people/"+name+"/display", strings.NewReader(string(body)), "application/json")
}

func (e *env) peopleRaw() map[string]map[string]any {
	e.t.Helper()
	rec := e.get("/api/train/people")
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		People []map[string]any `json:"people"`
	}
	require.NoError(e.t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	out := map[string]map[string]any{}
	for _, p := range resp.People {
		name, _ := p["name"].(string)
		out[name] = p
	}
	return out
}

func (e *env) displayOf(name string) (string, bool) {
	e.t.Helper()
	p, ok := e.peopleRaw()[name]
	require.True(e.t, ok, "person %s not listed", name)
	v, present := p["display_name"]
	if !present {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

func (e *env) cardDisplay(name string) string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.cfg.LoraDir, name+".person.json"))
	require.NoError(e.t, err)
	var card map[string]any
	require.NoError(e.t, json.Unmarshal(data, &card), string(data))
	s, _ := card["display_name"].(string)
	return s
}

func respPerson(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp struct {
		Person map[string]any `json:"person"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	require.NotNil(t, resp.Person, rec.Body.String())
	return resp.Person
}

func writePersonDisplay(t *testing.T, loraDir, name, base, display string) {
	t.Helper()
	writePerson(t, loraDir, name, "woman", base, 2000)
	if display == "" {
		return
	}
	path := filepath.Join(loraDir, name+".person.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var card map[string]any
	require.NoError(t, json.Unmarshal(data, &card))
	card["display_name"] = display
	data, err = json.Marshal(card)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func TestDisplay_Set(t *testing.T) {
	forty := strings.Repeat("Я", 40)
	tests := []struct {
		name    string
		display string
		want    string
	}{
		{"cyrillic", "Наташа", "Наташа"},
		{"trimmed", "  Наташа \t", "Наташа"},
		{"inner spaces collapsed", "Анна   Мария", "Анна Мария"},
		{"forty runes", forty, forty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "natasha", "woman", baseZImage, 2000)

			rec := e.setDisplay("natasha", tt.display, false)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			p := respPerson(t, rec)
			assert.Equal(t, "natasha", p["name"])
			assert.Equal(t, tt.want, p["display_name"])

			got, ok := e.displayOf("natasha")
			assert.True(t, ok)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want, e.cardDisplay("natasha"))
		})
	}
}

func TestDisplay_EmptyClears(t *testing.T) {
	for _, display := range []string{"", "   ", "\t "} {
		t.Run("q"+display, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "natasha", baseZImage, "Наташа")

			rec := e.setDisplay("natasha", display, false)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			p := respPerson(t, rec)
			_, present := p["display_name"]
			assert.False(t, present, rec.Body.String())

			got, ok := e.displayOf("natasha")
			assert.False(t, ok)
			assert.Empty(t, got)
			assert.Empty(t, e.cardDisplay("natasha"))
		})
	}
}

func TestDisplay_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		display string
	}{
		{"41 runes", strings.Repeat("Я", 41)},
		{"newline", "Ната\nша"},
		{"bell", "Наташа\x07"},
		{"nul", "Ната\x00ша"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "natasha", baseZImage, "Наташа")

			rec := e.setDisplay("natasha", tt.display, false)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_request", errorCode(t, rec))
			assert.Equal(t, "Наташа", e.cardDisplay("natasha"))
		})
	}
}

func TestDisplay_NotFound(t *testing.T) {
	for _, name := range []string{"nobody", "a", "bad!name", strings.Repeat("a", 33)} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			writePerson(t, e.cfg.LoraDir, "natasha", "woman", baseZImage, 2000)

			rec := e.setDisplay(name, "Наташа", false)
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.Equal(t, "not_found", errorCode(t, rec))
			_, ok := e.displayOf("natasha")
			assert.False(t, ok)
		})
	}
}

func TestDisplay_TakenOnSameBase(t *testing.T) {
	tests := []struct {
		name      string
		otherBase string
		thisBase  string
		display   string
	}{
		{"exact", baseSDXL, baseSDXL, "Наташа"},
		{"case-insensitive", baseSDXL, baseSDXL, "НАТАША"},
		{"lower case", baseSDXL, baseSDXL, "наташа"},
		{"spaces normalized", baseSDXL, baseSDXL, "  Наташа  "},
		{"empty base is Z-Image", "", baseZImage, "Наташа"},
		{"Z-Image vs empty base", baseZImage, "", "Наташа"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "natasha_old", tt.otherBase, "Наташа")
			writePersonDisplay(t, e.cfg.LoraDir, "natasha_new", tt.thisBase, "")

			rec := e.setDisplay("natasha_new", tt.display, false)
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "display_taken", errorCode(t, rec))
			var resp struct {
				Error errorView `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Contains(t, resp.Error.Message, "natasha_old")

			old, ok := e.displayOf("natasha_old")
			assert.True(t, ok)
			assert.Equal(t, "Наташа", old)
			_, ok = e.displayOf("natasha_new")
			assert.False(t, ok)
		})
	}
}

func TestDisplay_ReplaceMovesName(t *testing.T) {
	tests := []struct {
		name    string
		display string
	}{
		{"exact", "Наташа"},
		{"other case keeps existing spelling", "наташа"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "natasha_old", baseZImage, "Наташа")
			writePersonDisplay(t, e.cfg.LoraDir, "natasha_new", baseZImage, "")
			writePersonDisplay(t, e.cfg.LoraDir, "natasha_sdxl", baseSDXL, "Наташа")

			rec := e.setDisplay("natasha_new", tt.display, true)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "Наташа", respPerson(t, rec)["display_name"])

			got, ok := e.displayOf("natasha_new")
			assert.True(t, ok)
			assert.Equal(t, "Наташа", got)
			_, ok = e.displayOf("natasha_old")
			assert.False(t, ok)
			assert.Empty(t, e.cardDisplay("natasha_old"))
			sdxl, ok := e.displayOf("natasha_sdxl")
			assert.True(t, ok)
			assert.Equal(t, "Наташа", sdxl)
		})
	}
}

func TestDisplay_SameCardSameName(t *testing.T) {
	tests := []struct {
		name    string
		display string
		replace bool
	}{
		{"exact", "Наташа", false},
		{"exact replace", "Наташа", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "natasha", baseZImage, "Наташа")

			rec := e.setDisplay("natasha", tt.display, tt.replace)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "Наташа", respPerson(t, rec)["display_name"])
			got, ok := e.displayOf("natasha")
			assert.True(t, ok)
			assert.Equal(t, "Наташа", got)
		})
	}
}

func TestDisplay_OtherBaseAllowed(t *testing.T) {
	tests := []struct {
		name      string
		otherBase string
		thisBase  string
	}{
		{"Z-Image vs SDXL", baseZImage, baseSDXL},
		{"empty vs Flux.1", "", baseFlux1},
		{"SDXL vs Moss", baseSDXL, baseMoss},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "natasha_a", tt.otherBase, "Наташа")
			writePersonDisplay(t, e.cfg.LoraDir, "natasha_b", tt.thisBase, "")

			rec := e.setDisplay("natasha_b", "Наташа", false)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			a, ok := e.displayOf("natasha_a")
			assert.True(t, ok)
			assert.Equal(t, "Наташа", a)
			b, ok := e.displayOf("natasha_b")
			assert.True(t, ok)
			assert.Equal(t, "Наташа", b)
		})
	}
}

func TestDisplay_RenameKeepsName(t *testing.T) {
	e := newEnv(t)
	writePersonDisplay(t, e.cfg.LoraDir, "natasha", baseSDXL, "Наташа")

	rec := e.rename("natasha", `{"name":"natasha_sdxl"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "Наташа", respPerson(t, rec)["display_name"])

	got, ok := e.displayOf("natasha_sdxl")
	assert.True(t, ok)
	assert.Equal(t, "Наташа", got)
	assert.Equal(t, "Наташа", e.cardDisplay("natasha_sdxl"))
}

func TestDisplay_CardWithoutFieldReads(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 2000)

	people := e.people()
	require.Len(t, people, 1)
	assert.Equal(t, "anna", people[0].Name)
	got, _ := e.displayOf("anna")
	assert.Empty(t, got)

	rec := e.setDisplay("anna", "Анна", false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, ok := e.displayOf("anna")
	assert.True(t, ok)
	assert.Equal(t, "Анна", got)
}

func displayFields(name, display string) map[string]string {
	f := personFields(name, "woman")
	f["display_name"] = display
	return f
}

func TestDisplay_JobCreateSetsCard(t *testing.T) {
	tests := []struct {
		name    string
		display string
		want    string
	}{
		{"plain", "Наташа", "Наташа"},
		{"normalized", "  Анна   Мария ", "Анна Мария"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.post(displayFields("natasha", tt.display), photos(t, 10))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			j := decodeJob(t, rec)
			e.waitStarted()
			e.finishJob(j.ID())

			got, ok := e.displayOf("natasha")
			assert.True(t, ok)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want, e.cardDisplay("natasha"))
		})
	}
}

func TestDisplay_JobCreateWithoutName(t *testing.T) {
	e := newEnv(t)
	j := e.submit("natasha", "woman")
	e.waitStarted()
	e.finishJob(j.ID())

	got, ok := e.displayOf("natasha")
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestDisplay_JobCreateInvalid(t *testing.T) {
	for _, display := range []string{strings.Repeat("Я", 41), "Ната\nша"} {
		t.Run(display, func(t *testing.T) {
			e := newEnv(t)
			rec := e.post(displayFields("natasha", display), photos(t, 10))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_request", errorCode(t, rec))
			assert.Empty(t, e.jobsOf(testClient))
		})
	}
}

func TestDisplay_JobCreateTaken(t *testing.T) {
	tests := []struct {
		name     string
		cardBase string
		display  string
	}{
		{"same base", baseZImage, "Наташа"},
		{"empty base card", "", "наташа"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			writePersonDisplay(t, e.cfg.LoraDir, "natasha_old", tt.cardBase, "Наташа")

			rec := e.post(displayFields("natasha", tt.display), photos(t, 10))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "display_taken", errorCode(t, rec))
			assert.Empty(t, e.jobsOf(testClient))
		})
	}
}

func TestDisplay_JobCreateOtherBaseAllowed(t *testing.T) {
	e := newEnv(t)
	writePersonDisplay(t, e.cfg.LoraDir, "natasha_sdxl", baseSDXL, "Наташа")

	rec := e.post(displayFields("natasha", "Наташа"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := decodeJob(t, rec)
	e.waitStarted()
	e.finishJob(j.ID())

	got, ok := e.displayOf("natasha")
	assert.True(t, ok)
	assert.Equal(t, "Наташа", got)
}

func TestDisplay_JobNameTakenBeforeFinish(t *testing.T) {
	e := newEnv(t)
	rec := e.post(displayFields("natasha", "Наташа"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := decodeJob(t, rec)
	e.waitStarted()

	writePersonDisplay(t, e.cfg.LoraDir, "natasha_other", baseZImage, "")
	busy := e.setDisplay("natasha_other", "Наташа", false)
	require.Equal(t, http.StatusConflict, busy.Code, busy.Body.String())
	assert.Contains(t, busy.Body.String(), "display_taken")
	set := e.setDisplay("natasha_other", "Наташа", true)
	require.Equal(t, http.StatusOK, set.Code, set.Body.String())
	e.m.mu.Lock()
	for _, mj := range e.m.jobs {
		assert.Empty(t, mj.DisplayName)
	}
	e.m.mu.Unlock()

	e.finishJob(j.ID())
	assert.Equal(t, "completed", e.job(j.ID()).Status)
	assert.FileExists(t, e.loraPath("natasha"))

	got, ok := e.displayOf("natasha")
	assert.False(t, ok)
	assert.Empty(t, got)
	other, ok := e.displayOf("natasha_other")
	assert.True(t, ok)
	assert.Equal(t, "Наташа", other)
}

func TestDisplay_SubmitConflicts(t *testing.T) {
	e := newEnv(t)
	writePersonDisplay(t, e.cfg.LoraDir, "natasha_sdxl", baseSDXL, "Наташа")
	e.m.mu.Lock()
	e.m.jobs["j1"] = &Job{ID: "j1", Name: "natasha_a", Base: baseZImage, DisplayName: "Наташа", Status: statusRunning}
	e.m.mu.Unlock()

	e.m.mu.Lock()
	assert.Equal(t, "Наташа", e.m.canonicalDisplayLocked("наташа"))
	assert.Equal(t, "natasha_a", e.m.displayJobHolderLocked("НАТАША", "", "natasha_b"))
	assert.Equal(t, "", e.m.displayJobHolderLocked("Наташа", baseSDXL, "natasha_b"))
	assert.Equal(t, "", e.m.displayJobHolderLocked("Наташа", baseZImage, "natasha_a"))
	e.m.mu.Unlock()
}

func TestDisplay_TechNameClash(t *testing.T) {
	e := newEnv(t)
	writePersonDisplay(t, e.cfg.LoraDir, "anna", baseZImage, "")
	writePersonDisplay(t, e.cfg.LoraDir, "maria", baseZImage, "")
	writePersonDisplay(t, e.cfg.LoraDir, "alina", baseSDXL, "Lena")

	rec := e.setDisplay("maria", "Anna", false)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	rec = e.setDisplay("maria", "Anna", true)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	rec = e.setDisplay("anna", "anna", false)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	sub := e.post(displayFields("lena", ""), photos(t, 10))
	assert.Equal(t, http.StatusConflict, sub.Code, sub.Body.String())
	assert.Contains(t, sub.Body.String(), "name_taken")
	same := e.post(displayFields("lena", "lena"), photos(t, 10))
	assert.Equal(t, http.StatusAccepted, same.Code, same.Body.String())

	writePersonDisplay(t, e.cfg.LoraDir, "olga", baseSDXL, "Kira")
	_, status, code := e.m.renamePerson("maria", "kira")
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "name_taken", code)
	_, status, _ = e.m.renamePerson("olga", "kira")
	assert.Equal(t, http.StatusOK, status)
}

func TestDisplay_SubmitReplaceMovesName(t *testing.T) {
	e := newEnv(t)
	first := e.post(displayFields("natasha_a", "Наташа"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	ja := decodeJob(t, first)
	e.waitStarted()

	busy := e.post(displayFields("natasha_b", "наташа"), photos(t, 10))
	require.Equal(t, http.StatusConflict, busy.Code, busy.Body.String())
	assert.Contains(t, busy.Body.String(), "display_taken")

	f := displayFields("natasha_b", "наташа")
	f["display_replace"] = "1"
	second := e.post(f, photos(t, 10))
	require.Equal(t, http.StatusAccepted, second.Code, second.Body.String())
	jb := decodeJob(t, second)

	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(ja.ID(), "completed")
	got, ok := e.displayOf("natasha_a")
	assert.False(t, ok, got)

	e.waitStarted()
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(jb.ID(), "completed")
	e.m.Wait()
	got, ok = e.displayOf("natasha_b")
	assert.True(t, ok)
	assert.Equal(t, "Наташа", got)
}
