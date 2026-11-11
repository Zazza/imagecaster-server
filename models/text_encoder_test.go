package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

type teEnv struct {
	mux     *http.ServeMux
	teDir   string
	sdDir   string
	vaeDir  string
	loraDir string
}

func newTEEnv(t *testing.T, withTEDir bool) teEnv {
	t.Helper()
	return newTEEnvAt(t, t.TempDir(), withTEDir)
}

func newTEEnvAt(t *testing.T, root string, withTEDir bool) teEnv {
	t.Helper()
	sdDir := filepath.Join(root, "models", "Stable-diffusion")
	vaeDir := filepath.Join(root, "models", "VAE")
	b := config.BackendConfig{
		Name:      "Test",
		ModelsDir: sdDir,
		LoraDir:   filepath.Join(root, "models", "Lora"),
		VaeDir:    vaeDir,
	}
	teDir := filepath.Join(root, "models", "text_encoder")
	if withTEDir {
		teDir = filepath.Join(root, "encoders")
		b.TextEncoderDir = teDir
	}
	for _, d := range []string{sdDir, vaeDir, b.LoraDir, teDir} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	cfg := &config.Config{ActiveSD: "test", Backends: map[string]config.BackendConfig{"test": b}}
	mm := NewModelManager(cfg)
	mux := http.NewServeMux()
	mm.RegisterRoutes(mux)
	mm.RegisterDeleteRoutes(mux)
	return teEnv{mux: mux, teDir: teDir, sdDir: sdDir, vaeDir: vaeDir, loraDir: b.LoraDir}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
}

func listNames(t *testing.T, mux *http.ServeMux, path string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list []ModelInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		var wrapped map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &wrapped), rec.Body.String())
		found := false
		for _, raw := range wrapped {
			if json.Unmarshal(raw, &list) == nil {
				found = true
				break
			}
		}
		require.True(t, found, "no model list in %s", rec.Body.String())
	}
	names := make([]string, 0, len(list))
	for _, m := range list {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	return names
}

func fileServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func eventuallySize(t *testing.T, path string, size int) {
	t.Helper()
	require.Eventually(t, func() bool {
		info, err := os.Stat(path)
		return err == nil && info.Size() == int64(size)
	}, 5*time.Second, 20*time.Millisecond, "file %s not downloaded", path)
}

func TestTextEncoderList(t *testing.T) {
	for _, withTEDir := range []bool{true, false} {
		t.Run(fmt.Sprintf("text_encoder_dir=%v", withTEDir), func(t *testing.T) {
			e := newTEEnv(t, withTEDir)
			writeFile(t, filepath.Join(e.teDir, "a.gguf"))
			writeFile(t, filepath.Join(e.teDir, "qwen_3_06b_base.safetensors"))
			writeFile(t, filepath.Join(e.teDir, "moss.tokenizer.json"))
			writeFile(t, filepath.Join(e.teDir, "readme.txt"))
			writeFile(t, filepath.Join(e.sdDir, "model.safetensors"))

			got := listNames(t, e.mux, "/api/server/models/text_encoder")
			assert.Equal(t, []string{"a.gguf", "moss.tokenizer.json", "qwen_3_06b_base.safetensors"}, got)
		})
	}
}

func TestTextEncoderList_Empty(t *testing.T) {
	e := newTEEnv(t, true)
	assert.Empty(t, listNames(t, e.mux, "/api/server/models/text_encoder"))
}

func TestTextEncoderPostDownload(t *testing.T) {
	for _, withTEDir := range []bool{true, false} {
		t.Run(fmt.Sprintf("text_encoder_dir=%v", withTEDir), func(t *testing.T) {
			e := newTEEnv(t, withTEDir)
			content := bytes.Repeat([]byte("T"), 2048)
			srv := fileServer(t, content)

			payload, err := json.Marshal(map[string]string{"url": srv.URL + "/enc", "filename": "enc.gguf"})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/server/models/text_encoder", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			e.mux.ServeHTTP(rec, req)
			require.Less(t, rec.Code, 300, rec.Body.String())

			eventuallySize(t, filepath.Join(e.teDir, "enc.gguf"), len(content))
			_, err = os.Stat(filepath.Join(e.sdDir, "enc.gguf"))
			assert.True(t, os.IsNotExist(err), "must not land in ModelsDir")
		})
	}
}

func TestTextEncoderDownloadStream(t *testing.T) {
	for _, withTEDir := range []bool{true, false} {
		t.Run(fmt.Sprintf("text_encoder_dir=%v", withTEDir), func(t *testing.T) {
			e := newTEEnv(t, withTEDir)
			content := bytes.Repeat([]byte("J"), 777)
			srv := fileServer(t, content)

			q := url.Values{"type": {"text_encoder"}, "url": {srv.URL + "/tok"}, "filename": {"moss.tokenizer.json"}}
			rec := httptest.NewRecorder()
			e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server/models/download/stream?"+q.Encode(), nil))

			assert.Contains(t, rec.Body.String(), "data: [DONE]")
			assert.NotContains(t, rec.Body.String(), "[ERROR]")
			eventuallySize(t, filepath.Join(e.teDir, "moss.tokenizer.json"), len(content))
		})
	}
}

func TestTextEncoderDelete(t *testing.T) {
	for _, withTEDir := range []bool{true, false} {
		t.Run(fmt.Sprintf("text_encoder_dir=%v", withTEDir), func(t *testing.T) {
			e := newTEEnv(t, withTEDir)
			target := filepath.Join(e.teDir, "a.gguf")
			keep := filepath.Join(e.teDir, "b.gguf")
			sameNameInSD := filepath.Join(e.sdDir, "a.gguf")
			writeFile(t, target)
			writeFile(t, keep)
			writeFile(t, sameNameInSD)

			rec := httptest.NewRecorder()
			e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/server/models/delete/text_encoder/a.gguf", nil))
			require.Less(t, rec.Code, 300, rec.Body.String())

			_, err := os.Stat(target)
			assert.True(t, os.IsNotExist(err), "file must be deleted")
			assert.FileExists(t, keep)
			assert.FileExists(t, sameNameInSD)
		})
	}
}

func TestTextEncoderDelete_NotFound(t *testing.T) {
	e := newTEEnv(t, true)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/server/models/delete/text_encoder/missing.gguf", nil))
	assert.GreaterOrEqual(t, rec.Code, 400)
}

func TestTextEncoderDelete_Traversal(t *testing.T) {
	e := newTEEnv(t, true)
	outside := filepath.Join(e.sdDir, "model.gguf")
	writeFile(t, outside)
	rel, err := filepath.Rel(e.teDir, outside)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(rel, ".."))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/server/models/delete/text_encoder/x", nil)
	req.URL.Path = "/api/server/models/delete/text_encoder/" + filepath.ToSlash(rel)
	e.mux.ServeHTTP(rec, req)
	assert.FileExists(t, outside)
}

func TestModelListsShowGGUF(t *testing.T) {
	e := newTEEnv(t, true)
	writeFile(t, filepath.Join(e.sdDir, "flux3-image-klein-Q8_0.gguf"))
	writeFile(t, filepath.Join(e.sdDir, "sdxl.safetensors"))
	writeFile(t, filepath.Join(e.vaeDir, "ae.gguf"))
	writeFile(t, filepath.Join(e.vaeDir, "flux2-klein-vae.safetensors"))
	writeFile(t, filepath.Join(e.loraDir, "style.gguf"))

	cases := []struct {
		path string
		want []string
	}{
		{"/api/server/models/sd", []string{"flux3-image-klein-Q8_0.gguf", "sdxl.safetensors"}},
		{"/api/server/models/vae", []string{"ae.gguf", "flux2-klein-vae.safetensors"}},
		{"/api/server/models/lora", []string{"style.gguf"}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := listNames(t, e.mux, tc.path)
			for _, w := range tc.want {
				assert.Contains(t, got, w)
			}
		})
	}
}

func filesOutside(t *testing.T, base, dir string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			out = append(out, p)
		}
		return nil
	}))
	return out
}

func TestDownloadFilenameStaysInDir(t *testing.T) {
	content := bytes.Repeat([]byte("P"), 333)
	srv := fileServer(t, content)

	targets := []struct {
		typ  string
		post string
		dir  func(teEnv) string
	}{
		{"sd", "/api/server/models/sd", func(e teEnv) string { return e.sdDir }},
		{"vae", "/api/server/models/vae", func(e teEnv) string { return e.vaeDir }},
		{"text_encoder", "/api/server/models/text_encoder", func(e teEnv) string { return e.teDir }},
	}
	filenames := []struct {
		name       string
		filename   string
		mustReject bool
	}{
		{"traversal", "../../x.safetensors", false},
		{"dotdot", "..", true},
		{"dot", ".", true},
	}
	methods := []string{"post", "stream"}

	for _, tg := range targets {
		for _, fn := range filenames {
			for _, method := range methods {
				t.Run(tg.typ+"/"+fn.name+"/"+method, func(t *testing.T) {
					t.Parallel()
					base := t.TempDir()
					e := newTEEnvAt(t, filepath.Join(base, "a", "b"), false)
					dir := tg.dir(e)

					rec := httptest.NewRecorder()
					if method == "post" {
						payload, err := json.Marshal(map[string]string{"url": srv.URL + "/f", "filename": fn.filename})
						require.NoError(t, err)
						req := httptest.NewRequest(http.MethodPost, tg.post, bytes.NewReader(payload))
						req.Header.Set("Content-Type", "application/json")
						e.mux.ServeHTTP(rec, req)
					} else {
						q := url.Values{"type": {tg.typ}, "url": {srv.URL + "/f"}, "filename": {fn.filename}}
						e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server/models/download/stream?"+q.Encode(), nil))
					}

					if fn.mustReject {
						assert.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
					} else if rec.Code < 400 && !strings.Contains(rec.Body.String(), "[ERROR]") {
						eventuallySize(t, filepath.Join(dir, "x.safetensors"), len(content))
					}
					time.Sleep(300 * time.Millisecond)
					assert.Empty(t, filesOutside(t, base, dir), "file created outside %s", dir)
				})
			}
		}
	}
}

func TestDelete_NoActiveBackend(t *testing.T) {
	for _, typ := range []string{"sd", "lora", "vae", "text_encoder"} {
		t.Run(typ, func(t *testing.T) {
			cwd := t.TempDir()
			victim := filepath.Join(cwd, "a.gguf")
			writeFile(t, victim)
			t.Chdir(cwd)

			cfg := &config.Config{ActiveSD: "nonexistent", Backends: map[string]config.BackendConfig{}}
			mm := NewModelManager(cfg)
			mux := http.NewServeMux()
			mm.RegisterRoutes(mux)
			mm.RegisterDeleteRoutes(mux)

			rec := httptest.NewRecorder()
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/server/models/delete/"+typ+"/a.gguf", nil))
			}()

			require.Nil(t, panicked, "handler panicked")
			assert.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
			assert.FileExists(t, victim)
		})
	}
}

func TestPostDownload_ResponseFilenameSanitized(t *testing.T) {
	content := bytes.Repeat([]byte("R"), 128)
	srv := fileServer(t, content)
	base := t.TempDir()
	e := newTEEnvAt(t, filepath.Join(base, "a", "b"), false)

	payload, err := json.Marshal(map[string]string{"url": srv.URL + "/f", "filename": "../../x.safetensors"})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/server/models/sd", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	e.mux.ServeHTTP(rec, req)
	require.Less(t, rec.Code, 300, rec.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	assert.Equal(t, "x.safetensors", body["filename"])

	eventuallySize(t, filepath.Join(e.sdDir, "x.safetensors"), len(content))
}
