package llamacpp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const shared = "qwen2.5-chat-ru:latest"

func foreignStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	addModel(t, root, "gemma3:12b", "gemma3",
		fixtureLayer{MediaModel, []byte("GGUF-gemma-foreign")},
		fixtureLayer{MediaSystem, []byte("You are Gemma.")},
		fixtureLayer{MediaParams, []byte(`{"temperature":0.9}`)})
	addModel(t, root, "qwen2.5-chat-ru", "qwen2", fixtureLayer{MediaModel, []byte("GGUF-ru-foreign")})
	addModel(t, root, "llava-own:7b", "qwen25vl", fixtureLayer{MediaModel, []byte("GGUF-foreign-own-format")})
	return &Store{Root: root, ReadOnly: true}
}

func snapshot(t *testing.T, root string) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if !d.IsDir() {
			st, err := os.Stat(p)
			require.NoError(t, err)
			out[p] = st.ModTime()
		}
		return nil
	}))
	return out
}

func names(list []Model) map[string]Model {
	out := map[string]Model{}
	for _, m := range list {
		out[m.Name.String()] = m
	}
	return out
}

func TestLibraryListMergesAndOwnWins(t *testing.T) {
	own, foreign := fixtureStore(t), foreignStore(t)
	lib := &Library{Own: own, Foreign: []*Store{foreign}}
	list, err := lib.List()
	require.NoError(t, err)
	byName := names(list)
	assert.Len(t, list, len(byName), "no duplicates")
	assert.Contains(t, byName, "gemma3:12b")
	assert.Contains(t, byName, qwen)
	require.Contains(t, byName, shared)
	assert.False(t, byName[shared].ReadOnly, "own copy wins")
	assert.True(t, strings.HasPrefix(byName[shared].ModelPath, own.Root))
	assert.True(t, byName["gemma3:12b"].ReadOnly)
}

func TestLibraryMissingForeignDirIsFine(t *testing.T) {
	lib := &Library{Own: fixtureStore(t), Foreign: []*Store{{Root: filepath.Join(t.TempDir(), "nope"), ReadOnly: true}}}
	list, err := lib.List()
	require.NoError(t, err)
	assert.NotEmpty(t, list)
	_, err = lib.Resolve("nope:1")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestLibraryResolve(t *testing.T) {
	lib := &Library{Own: fixtureStore(t), Foreign: []*Store{foreignStore(t)}}
	tests := []struct {
		name     string
		query    string
		want     string
		readOnly bool
	}{
		{"own exact", qwen, qwen, false},
		{"foreign exact", "gemma3:12b", "gemma3:12b", true},
		{"foreign other case", "Gemma3:12B", "gemma3:12b", true},
		{"duplicate gives own", "qwen2.5-chat-ru", shared, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := lib.Resolve(tt.query)
			require.NoError(t, err)
			assert.Equal(t, tt.want, m.Name.String())
			assert.Equal(t, tt.readOnly, m.ReadOnly)
		})
	}
}

func TestLibraryDeleteForeignIsRefused(t *testing.T) {
	foreign := foreignStore(t)
	lib := &Library{Own: fixtureStore(t), Foreign: []*Store{foreign}}
	before := snapshot(t, foreign.Root)
	err := lib.Delete("gemma3:12b")
	require.ErrorIs(t, err, ErrReadOnly)
	assert.Contains(t, err.Error(), "ollama rm gemma3:12b")
	assert.Contains(t, err.Error(), foreign.Root)
	assert.Equal(t, before, snapshot(t, foreign.Root))
}

func TestLibraryDeleteOwnDuplicateRevealsForeign(t *testing.T) {
	foreign := foreignStore(t)
	lib := &Library{Own: fixtureStore(t), Foreign: []*Store{foreign}}
	require.NoError(t, lib.Delete(shared))
	m, err := lib.Resolve(shared)
	require.NoError(t, err)
	assert.True(t, m.ReadOnly)
	assert.True(t, strings.HasPrefix(m.ModelPath, foreign.Root))
}

func TestLibraryForeignTextAndParamsFromItsBlobs(t *testing.T) {
	lib := &Library{Own: fixtureStore(t), Foreign: []*Store{foreignStore(t)}}
	m, err := lib.Resolve("gemma3:12b")
	require.NoError(t, err)
	assert.Equal(t, "You are Gemma.", lib.Text(m, MediaSystem))
	assert.Equal(t, 0.9, lib.Params(m)["temperature"])
}

func TestLibraryWritePresetHasForeignBlobPaths(t *testing.T) {
	foreign := foreignStore(t)
	lib := &Library{Own: fixtureStore(t), Foreign: []*Store{foreign}}
	path := filepath.Join(t.TempDir(), "models.ini")
	ids, err := lib.WritePreset(path, PresetOptions{})
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	m, err := lib.Resolve("gemma3:12b")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(m.ModelPath))
	assert.Contains(t, string(raw), "model = "+m.ModelPath)
	assert.Contains(t, ids, "gemma3:12b")
	assert.NotContains(t, ids, "llava-own:7b")
}

func newForeignFacadeEnv(t *testing.T) (*facadeEnv, *Store) {
	t.Helper()
	foreign := foreignStore(t)
	e := &facadeEnv{fr: newFakeRouter(t), store: fixtureStore(t), now: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	e.f = New(Config{
		Store:      e.store,
		Foreign:    []*Store{foreign},
		RouterURL:  e.fr.srv.URL,
		PresetPath: filepath.Join(t.TempDir(), "models.ini"),
		KeepAlive:  5 * time.Minute,
		Reload:     func(context.Context) error { e.fr.setIDs(e.f.IDs()); return nil },
		Now:        func() time.Time { return e.now },
		Poll:       time.Millisecond,
	})
	require.NoError(t, e.f.Refresh(context.Background()))
	return e, foreign
}

func TestFacadeDeleteForeignIs409WithoutUnload(t *testing.T) {
	e, foreign := newForeignFacadeEnv(t)
	rec := e.do(t, http.MethodPost, "/api/chat", `{"model":"gemma3:12b","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	before := snapshot(t, foreign.Root)

	rec = e.do(t, http.MethodDelete, "/api/delete", `{"model":"gemma3:12b"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "ollama rm")
	e.fr.mu.Lock()
	assert.Empty(t, e.fr.unloads)
	e.fr.mu.Unlock()
	assert.Equal(t, before, snapshot(t, foreign.Root))
	assert.Contains(t, e.f.IDs(), "gemma3:12b")
}

func TestFacadeListsHideUnsupported(t *testing.T) {
	e, _ := newForeignFacadeEnv(t)
	rec := e.do(t, http.MethodGet, "/api/tags", "")
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"gemma3:12b"`)
	assert.NotContains(t, rec.Body.String(), "llava-own")
	assert.NotContains(t, rec.Body.String(), "qwen2.5vl")

	rec = e.do(t, http.MethodGet, "/v1/models", "")
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"gemma3:12b"`)
	assert.NotContains(t, rec.Body.String(), "llava-own")
	assert.NotContains(t, rec.Body.String(), "qwen2.5vl")
}

func TestServerModelsMarksReadOnlyAndUnsupported(t *testing.T) {
	e, _ := newForeignFacadeEnv(t)
	req, _ := http.NewRequest(http.MethodGet, RouteServerLLM, nil)
	rec := httptest.NewRecorder()
	e.f.ServeServerModels(rec, req)
	require.Equal(t, 200, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	byName := map[string]map[string]any{}
	for _, m := range out {
		byName[m["name"].(string)] = m
	}
	require.Contains(t, byName, "gemma3:12b")
	assert.Equal(t, true, byName["gemma3:12b"]["readonly"])
	require.Contains(t, byName, shared)
	assert.Nil(t, byName[shared]["readonly"])
	require.Contains(t, byName, "qwen2.5vl:7b")
	assert.Contains(t, byName["qwen2.5vl:7b"]["unsupported"], "Ollama's own format")
	assert.NotContains(t, byName, "llava-own:7b")
}

func TestFacadePullWritesOnlyOwn(t *testing.T) {
	e, foreign := newForeignFacadeEnv(t)
	reg := newFakeRegistry()
	reg.add("library/newm", "latest", "qwen2", fixtureLayer{MediaModel, []byte("GGUF-new")})
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	e.f.cfg.Puller = &Puller{Store: e.store, BaseURL: func(string) string { return srv.URL }}
	before := snapshot(t, foreign.Root)

	rec := e.do(t, http.MethodPost, "/api/pull", `{"model":"newm","stream":false}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	m, err := e.store.Resolve("newm")
	require.NoError(t, err)
	assert.False(t, m.ReadOnly)
	assert.Equal(t, before, snapshot(t, foreign.Root))
}

func TestLibraryTwoForeignDirs(t *testing.T) {
	tests := []struct {
		name     string
		own      []string
		first    []string
		second   []string
		query    string
		listName string
		want     int
	}{
		{name: "same model in both foreign, no own", first: []string{"dup:1"}, second: []string{"dup:1"}, query: "dup:1", listName: "dup:1", want: 1},
		{name: "exact in second foreign beats own other case", own: []string{"Exact:7B"}, first: []string{"Exact:7b"}, second: []string{"exact:7b"}, query: "exact:7b", want: 2},
		{name: "own beats both foreign", own: []string{"dup:1"}, first: []string{"dup:1"}, second: []string{"dup:1"}, query: "dup:1", listName: "dup:1", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roots := []string{t.TempDir(), t.TempDir(), t.TempDir()}
			for i, models := range [][]string{tt.own, tt.first, tt.second} {
				for _, n := range models {
					addModel(t, roots[i], n, "qwen2", fixtureLayer{MediaModel, []byte("GGUF-" + n + "-" + roots[i])})
				}
			}
			lib := &Library{
				Own:     &Store{Root: roots[0]},
				Foreign: []*Store{{Root: roots[1], ReadOnly: true}, {Root: roots[2], ReadOnly: true}},
			}
			wantRoot := roots[tt.want] + string(filepath.Separator)

			m, err := lib.Resolve(tt.query)
			require.NoError(t, err)
			assert.Equal(t, tt.query, m.Name.String())
			assert.Equal(t, tt.want != 0, m.ReadOnly)
			assert.True(t, strings.HasPrefix(m.ModelPath, wantRoot), m.ModelPath)

			if tt.listName == "" {
				return
			}
			list, err := lib.List()
			require.NoError(t, err)
			var found []Model
			for _, l := range list {
				if l.Name.String() == tt.listName {
					found = append(found, l)
				}
			}
			require.Len(t, found, 1)
			assert.Equal(t, tt.want != 0, found[0].ReadOnly)
			assert.True(t, strings.HasPrefix(found[0].ModelPath, wantRoot), found[0].ModelPath)
		})
	}
}
