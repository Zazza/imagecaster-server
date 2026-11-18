package models

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFaceType_ListsOnlyOnnxNextToModelsDir(t *testing.T) {
	root := t.TempDir()
	env := newTEEnvAt(t, root, false)
	faceDir := filepath.Join(filepath.Dir(env.sdDir), "face")
	writeFile(t, filepath.Join(faceDir, "det_10g.onnx"))
	writeFile(t, filepath.Join(faceDir, "inswapper_128.onnx"))
	writeFile(t, filepath.Join(faceDir, "notes.safetensors"))
	writeFile(t, filepath.Join(env.sdDir, "yolox_l.onnx"))
	writeFile(t, filepath.Join(env.sdDir, "model.safetensors"))

	assert.Equal(t, []string{"det_10g.onnx", "inswapper_128.onnx"}, listNames(t, env.mux, "/api/server/models/face"))
	assert.Equal(t, []string{"model.safetensors"}, listNames(t, env.mux, "/api/server/models/sd"))
}
