package models

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrainType_ListsFilesNextToModelsDir(t *testing.T) {
	root := t.TempDir()
	env := newTEEnvAt(t, root, false)
	trainDir := filepath.Join(filepath.Dir(env.sdDir), "train")
	writeFile(t, filepath.Join(trainDir, "z_image_turbo_bf16.safetensors"))
	writeFile(t, filepath.Join(trainDir, "zimage_turbo_training_adapter_v2.safetensors"))
	writeFile(t, filepath.Join(env.sdDir, "juggernautXL_ragnarokBy.safetensors"))
	writeFile(t, filepath.Join(env.vaeDir, "ae.safetensors"))

	assert.Equal(t,
		[]string{"z_image_turbo_bf16.safetensors", "zimage_turbo_training_adapter_v2.safetensors"},
		listNames(t, env.mux, "/api/server/models/train"))
	assert.Equal(t, []string{"juggernautXL_ragnarokBy.safetensors"}, listNames(t, env.mux, "/api/server/models/sd"))
}

func TestTrainType_UnknownTypeStill404(t *testing.T) {
	env := newTEEnvAt(t, t.TempDir(), false)
	for _, typ := range []string{"foo", "trains"} {
		t.Run(typ, func(t *testing.T) {
			rec := httptest.NewRecorder()
			env.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server/models/"+typ, nil))
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		})
	}
}

func TestTrainType_DownloadStreamCreatesDir(t *testing.T) {
	t.Parallel()

	content := bytes.Repeat([]byte("T"), 2048)
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2048")
		w.Write(content)
	}))
	defer fileServer.Close()

	root := t.TempDir()
	modelsDir := filepath.Join(root, "checkpoints")
	require.NoError(t, os.MkdirAll(modelsDir, 0o755))
	trainDir := filepath.Join(root, "train")
	require.NoDirExists(t, trainDir)
	mm := newTestModelManager(t, modelsDir)

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/server/models/download/stream?type=train&url=%s/adapter&filename=zimage_turbo_training_adapter_v2.safetensors", fileServer.URL), nil)
	rec := httptest.NewRecorder()

	mm.handleDownloadStream(rec, req)

	assert.Contains(t, rec.Body.String(), "data: [DONE]")
	info, err := os.Stat(filepath.Join(trainDir, "zimage_turbo_training_adapter_v2.safetensors"))
	require.NoError(t, err)
	assert.Equal(t, int64(2048), info.Size())
	assert.NoFileExists(t, filepath.Join(modelsDir, "zimage_turbo_training_adapter_v2.safetensors"))
}
