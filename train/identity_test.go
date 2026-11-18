package train

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/faceswap"
)

type fakeIdentity struct {
	available bool
	err       error
	log       *eventLog

	mu    sync.Mutex
	paths []string
	dirs  []string
}

func (f *fakeIdentity) Available() bool { return f.available }

func (f *fakeIdentity) FromImages(_ context.Context, paths []string) (faceswap.Identity, error) {
	if f.log != nil {
		f.log.add("identity")
	}
	f.mu.Lock()
	f.paths = append([]string(nil), paths...)
	if len(paths) > 0 {
		f.dirs = append(f.dirs, filepath.Dir(paths[0]))
	}
	f.mu.Unlock()
	if f.err != nil {
		return faceswap.Identity{}, f.err
	}
	emb := make([]float32, 512)
	emb[0] = 1
	return faceswap.Identity{Model: faceswap.IdentityModel, Embedding: emb, PhotosUsed: len(paths), CreatedAt: time.Now().UTC()}, nil
}

func (f *fakeIdentity) seen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...), append([]string(nil), f.dirs...)
}

func storedIdentity(t *testing.T, e *env, name string) *faceswap.Identity {
	t.Helper()
	pf, err := readPersonFile(e.m.personPath(name))
	require.NoError(t, err)
	return pf.Identity
}

func (e *env) postIdentity(name string, n int) *httptest.ResponseRecorder {
	e.t.Helper()
	body, ct := multipartBody(e.t, map[string]string{"client": testClient}, photos(e.t, n))
	return e.do(http.MethodPost, "/api/train/people/"+name+"/identity", body, ct)
}

func TestTraining_StoresIdentity(t *testing.T) {
	cases := []struct {
		name       string
		builder    *fakeIdentity
		wantStatus string
		wantStored bool
	}{
		{"done", &fakeIdentity{available: true}, "done", true},
		{"too few faces", &fakeIdentity{available: true, err: fmt.Errorf("%w: 1 of 3", faceswap.ErrTooFewFaces)}, "skipped:too_few_faces", false},
		{"models missing", &fakeIdentity{available: false}, "skipped:face_models_missing", false},
		{"engine failure", &fakeIdentity{available: true, err: fmt.Errorf("onnx crashed")}, "skipped:failed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.m.SetIdentityBuilder(tc.builder)
			j := e.submit("anna", "woman")
			e.waitStarted()
			e.finishRun(runResult{writeOutput: true})
			e.waitStatus(j.ID(), statusCompleted)

			rec := e.get("/api/train/jobs/" + j.ID())
			var job Job
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &job))
			assert.Equal(t, tc.wantStatus, job.Identity)
			id := storedIdentity(t, e, "anna")
			assert.Equal(t, tc.wantStored, id != nil)
			if tc.builder.available {
				paths, _ := tc.builder.seen()
				assert.Len(t, paths, 10)
			}
			require.Len(t, e.people(), 1)
		})
	}
}

func TestTraining_IdentityBeforeLease(t *testing.T) {
	e := newEnv(t)
	b := &fakeIdentity{available: true, log: e.log}
	e.m.SetIdentityBuilder(b)
	j := e.submit("anna", "woman")
	e.waitStarted()
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(j.ID(), statusCompleted)
	paths, _ := b.seen()
	require.NotEmpty(t, paths)
	for _, p := range paths {
		assert.Contains(t, []string{".jpg", ".png"}, filepath.Ext(p))
	}
	assert.Equal(t, []string{"identity", "lease", "run", "release"}, e.log.list())
}

func TestPeople_IdentityIsBoolOnly(t *testing.T) {
	e := newEnv(t)
	e.m.SetIdentityBuilder(&fakeIdentity{available: true})
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 10)
	writePerson(t, e.cfg.LoraDir, "bob", "man", baseZImage, 10)
	rec := e.postIdentity("anna", 3)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "embedding")

	rec = e.get("/api/train/people")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "embedding")
	var resp struct {
		People []map[string]any `json:"people"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.People, 2)
	assert.Equal(t, true, resp.People[0]["identity"])
	assert.Equal(t, false, resp.People[1]["identity"])
}

func TestRename_KeepsIdentity(t *testing.T) {
	e := newEnv(t)
	e.m.SetIdentityBuilder(&fakeIdentity{available: true})
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 10)
	require.Equal(t, http.StatusOK, e.postIdentity("anna", 3).Code)
	before := storedIdentity(t, e, "anna")
	require.NotNil(t, before)

	rec := e.rename("anna", `{"name":"maria"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "embedding")
	assert.Contains(t, rec.Body.String(), `"identity":true`)
	after := storedIdentity(t, e, "maria")
	require.NotNil(t, after)
	assert.Equal(t, before.Embedding, after.Embedding)
	assert.Equal(t, before.PhotosUsed, after.PhotosUsed)
	got, err := ReadIdentity(e.cfg.LoraDir, "maria")
	require.NoError(t, err)
	assert.Equal(t, before.Embedding, got)
}

func TestIdentityEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		person   string
		photos   int
		builder  *fakeIdentity
		wantHTTP int
		wantCode string
	}{
		{"ok min", "anna", 3, &fakeIdentity{available: true}, http.StatusOK, ""},
		{"ok max", "anna", 50, &fakeIdentity{available: true}, http.StatusOK, ""},
		{"too few photos", "anna", 2, &fakeIdentity{available: true}, http.StatusBadRequest, "invalid_request"},
		{"too many photos", "anna", 51, &fakeIdentity{available: true}, http.StatusBadRequest, "invalid_request"},
		{"too few faces", "anna", 5, &fakeIdentity{available: true, err: fmt.Errorf("%w: 2 of 3", faceswap.ErrTooFewFaces)}, http.StatusBadRequest, "too_few_faces"},
		{"models missing", "anna", 3, &fakeIdentity{available: false}, http.StatusServiceUnavailable, "face_models_missing"},
		{"unknown person", "zoe", 3, &fakeIdentity{available: true}, http.StatusNotFound, "not_found"},
		{"bad name", "..", 3, &fakeIdentity{available: true}, http.StatusNotFound, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.m.SetIdentityBuilder(tc.builder)
			writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 10)
			rec := e.postIdentity(tc.person, tc.photos)
			require.Equal(t, tc.wantHTTP, rec.Code, rec.Body.String())
			if tc.wantCode != "" {
				assert.Contains(t, rec.Body.String(), `"code":"`+tc.wantCode+`"`)
			}
			assert.Equal(t, tc.wantHTTP == http.StatusOK, storedIdentity(t, e, "anna") != nil)
			entries, err := os.ReadDir(e.cfg.JobsDir)
			require.NoError(t, err)
			for _, en := range entries {
				assert.False(t, strings.HasPrefix(en.Name(), "identity-"), "temp dir %s left behind", en.Name())
			}
			if _, dirs := tc.builder.seen(); len(dirs) > 0 {
				assert.NoDirExists(t, dirs[0])
			}
		})
	}
}

func TestReadIdentity(t *testing.T) {
	e := newEnv(t)
	e.m.SetIdentityBuilder(&fakeIdentity{available: true})
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 10)
	writePerson(t, e.cfg.LoraDir, "bob", "man", baseZImage, 10)
	require.Equal(t, http.StatusOK, e.postIdentity("anna", 3).Code)
	cases := []struct {
		name string
		want error
	}{
		{"anna", nil},
		{"bob", faceswap.ErrNoIdentity},
		{"zoe", faceswap.ErrPersonNotFound},
		{"../anna", faceswap.ErrPersonNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadIdentity(e.cfg.LoraDir, tc.name)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, 512)
		})
	}
}
