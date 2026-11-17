package models

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"imagecaster-server/api"
	"imagecaster-server/config"
)

func (m *ModelManager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/server/models/{type}", m.handleModels)
	mux.HandleFunc("/api/server/models/download/stream", m.handleDownloadStream)
}

var modelTypes = []string{"sd", "lora", "vae", "text_encoder", "train"}

func dirFor(b *config.BackendConfig, typ string) (string, bool) {
	if b == nil {
		return "", slices.Contains(modelTypes, typ)
	}
	switch typ {
	case "sd":
		return b.ModelsDir, true
	case "lora":
		return b.LoraDir, true
	case "vae":
		return b.VaeDir, true
	case "text_encoder":
		return textEncoderDir(b), true
	case "train":
		return TrainDir(b), true
	}
	return "", false
}

func (m *ModelManager) handleModels(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	backend := m.config.GetActiveBackend()
	dir, ok := dirFor(backend, typ)
	if !ok {
		api.WriteError(w, "unknown model type: "+typ, http.StatusNotFound)
		return
	}
	if backend == nil {
		api.WriteError(w, "no active backend", http.StatusBadRequest)
		return
	}
	if typ == "text_encoder" {
		m.handleTextEncoderModels(w, r, backend, dir)
		return
	}
	m.handleFileDir(w, r, dir, modelFileExts)
}

var textEncoderFileExts = map[string]bool{".safetensors": true, ".gguf": true, ".bin": true, ".json": true}

func TrainDir(b *config.BackendConfig) string {
	if b == nil || b.ModelsDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(b.ModelsDir), "train")
}

func textEncoderDir(b *config.BackendConfig) string {
	if b == nil {
		return ""
	}
	if b.TextEncoderDir != "" {
		return b.TextEncoderDir
	}
	if b.ModelsDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(b.ModelsDir), "text_encoder")
}

func (m *ModelManager) handleTextEncoderModels(w http.ResponseWriter, r *http.Request, backend *config.BackendConfig, dir string) {
	if r.Method != http.MethodGet || backend.TokenizerDir == "" || filepath.Clean(backend.TokenizerDir) == filepath.Clean(dir) {
		m.handleFileDir(w, r, dir, textEncoderFileExts)
		return
	}
	models, err := m.listFiles(dir, textEncoderFileExts)
	if err != nil {
		api.WriteError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tokenizers, err := m.listFiles(backend.TokenizerDir, map[string]bool{".json": true})
	if err != nil {
		api.WriteError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	seen := make(map[string]bool, len(models))
	for _, mi := range models {
		seen[mi.Name] = true
	}
	for _, t := range tokenizers {
		if !seen[t.Name] {
			models = append(models, t)
		}
	}
	api.WriteJSON(w, models)
}

func (m *ModelManager) handleFileDir(w http.ResponseWriter, r *http.Request, dir string, exts map[string]bool) {
	switch r.Method {
	case http.MethodGet:
		models, err := m.listFiles(dir, exts)
		if err != nil {
			api.WriteError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		api.WriteJSON(w, models)

	case http.MethodPost:
		var req struct {
			URL      string `json:"url"`
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			api.WriteError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.URL == "" || req.Filename == "" {
			api.WriteError(w, "url and filename are required", http.StatusBadRequest)
			return
		}
		name, err := safeFilename(req.Filename)
		if err != nil {
			api.WriteError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := parseSHA256(req.SHA256); err != nil {
			api.WriteError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := m.DownloadModel(r.Context(), req.URL, dir, name, req.SHA256); err != nil {
			api.WriteError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		api.WriteJSON(w, map[string]string{"status": "downloaded", "filename": name})

	default:
		api.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *ModelManager) RegisterDeleteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/server/models/delete/{type}/{name}", m.handleDelete)
}

func (m *ModelManager) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		api.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	typ := r.PathValue("type")
	b := m.config.GetActiveBackend()
	dir, ok := dirFor(b, typ)
	if !ok {
		api.WriteError(w, "unknown model type: "+typ, http.StatusNotFound)
		return
	}
	filename := filepath.Base(r.PathValue("name"))
	if typ == "text_encoder" && b != nil && b.TokenizerDir != "" && dir != "" && strings.EqualFold(filepath.Ext(filename), ".json") {
		if _, err := os.Stat(filepath.Join(dir, filename)); os.IsNotExist(err) {
			dir = b.TokenizerDir
		}
	}
	if err := m.DeleteModel(dir, filename); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			code = http.StatusNotFound
		}
		api.WriteError(w, err.Error(), code)
		return
	}
	api.WriteJSON(w, map[string]string{"status": "deleted", "filename": filename})
}
