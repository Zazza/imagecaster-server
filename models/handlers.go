package models

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"imagecaster-server/api"
	"imagecaster-server/config"
)

func (m *ModelManager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/server/models/sd", m.handleSDModels)
	mux.HandleFunc("/api/server/models/lora", m.handleLoRAModels)
	mux.HandleFunc("/api/server/models/vae", m.handleVAEModels)
	mux.HandleFunc("/api/server/models/text_encoder", m.handleTextEncoderModels)
	mux.HandleFunc("/api/server/models/llm", m.handleLLMModels)
	mux.HandleFunc("/api/server/models/llm/pull", m.handleLLMPullStream)
	mux.HandleFunc("/api/server/models/download/stream", m.handleDownloadStream)
}

func (m *ModelManager) handleSDModels(w http.ResponseWriter, r *http.Request) {
	backend := m.config.GetActiveBackend()
	if backend == nil {
		api.WriteError(w, "no active backend", http.StatusBadRequest)
		return
	}
	m.handleModelDir(w, r, backend.ModelsDir)
}

func (m *ModelManager) handleLoRAModels(w http.ResponseWriter, r *http.Request) {
	backend := m.config.GetActiveBackend()
	if backend == nil {
		api.WriteError(w, "no active backend", http.StatusBadRequest)
		return
	}
	m.handleModelDir(w, r, backend.LoraDir)
}

func (m *ModelManager) handleVAEModels(w http.ResponseWriter, r *http.Request) {
	backend := m.config.GetActiveBackend()
	if backend == nil {
		api.WriteError(w, "no active backend", http.StatusBadRequest)
		return
	}
	m.handleModelDir(w, r, backend.VaeDir)
}

var textEncoderFileExts = map[string]bool{".safetensors": true, ".gguf": true, ".bin": true, ".json": true}

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

func (m *ModelManager) handleTextEncoderModels(w http.ResponseWriter, r *http.Request) {
	backend := m.config.GetActiveBackend()
	if backend == nil {
		api.WriteError(w, "no active backend", http.StatusBadRequest)
		return
	}
	dir := textEncoderDir(backend)
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

func (m *ModelManager) handleModelDir(w http.ResponseWriter, r *http.Request, dir string) {
	m.handleFileDir(w, r, dir, modelFileExts)
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
		if err := m.DownloadModel(req.URL, dir, name); err != nil {
			api.WriteError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		api.WriteJSON(w, map[string]string{"status": "downloaded", "filename": name})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *ModelManager) RegisterDeleteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/server/models/delete/sd/", m.handleDeleteSD)
	mux.HandleFunc("/api/server/models/delete/lora/", m.handleDeleteLoRA)
	mux.HandleFunc("/api/server/models/delete/vae/", m.handleDeleteVAE)
	mux.HandleFunc("/api/server/models/delete/text_encoder/", m.handleDeleteTextEncoder)
	mux.HandleFunc("/api/server/models/delete/llm/", m.handleDeleteLLM)
}

func (m *ModelManager) activeDir(pick func(*config.BackendConfig) string) string {
	b := m.config.GetActiveBackend()
	if b == nil {
		return ""
	}
	return pick(b)
}

func (m *ModelManager) handleDeleteSD(w http.ResponseWriter, r *http.Request) {
	m.handleDeleteFromDir(w, r, m.activeDir(func(b *config.BackendConfig) string { return b.ModelsDir }))
}

func (m *ModelManager) handleDeleteLoRA(w http.ResponseWriter, r *http.Request) {
	m.handleDeleteFromDir(w, r, m.activeDir(func(b *config.BackendConfig) string { return b.LoraDir }))
}

func (m *ModelManager) handleDeleteVAE(w http.ResponseWriter, r *http.Request) {
	m.handleDeleteFromDir(w, r, m.activeDir(func(b *config.BackendConfig) string { return b.VaeDir }))
}

func (m *ModelManager) handleDeleteTextEncoder(w http.ResponseWriter, r *http.Request) {
	b := m.config.GetActiveBackend()
	dir := textEncoderDir(b)
	name := filepath.Base(r.URL.Path)
	if b != nil && b.TokenizerDir != "" && dir != "" && strings.EqualFold(filepath.Ext(name), ".json") {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			dir = b.TokenizerDir
		}
	}
	m.handleDeleteFromDir(w, r, dir)
}

func (m *ModelManager) handleDeleteFromDir(w http.ResponseWriter, r *http.Request, dir string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filename := strings.TrimPrefix(r.URL.Path, "/api/server/models/delete/")
	filename = strings.TrimPrefix(filename, "sd/")
	filename = strings.TrimPrefix(filename, "lora/")
	filename = strings.TrimPrefix(filename, "vae/")
	filename = strings.TrimPrefix(filename, "text_encoder/")
	filename = filepath.Base(filename)

	if err := m.DeleteModel(dir, filename); err != nil {
		api.WriteError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.WriteJSON(w, map[string]string{"status": "deleted", "filename": filename})
}

func (m *ModelManager) handleDeleteLLM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/server/models/delete/llm/")
	name = filepath.Base(name)

	if err := m.DeleteLLMModel(name); err != nil {
		api.WriteError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.WriteJSON(w, map[string]string{"status": "deleted", "name": name})
}
