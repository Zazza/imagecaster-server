package models

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"imagecaster-server/config"
)

type ModelManager struct {
	config *config.Config
	mu     sync.Mutex
	active map[string]bool
}

func NewModelManager(cfg *config.Config) *ModelManager {
	return &ModelManager{config: cfg}
}

var modelFileExts = map[string]bool{".safetensors": true, ".ckpt": true, ".pt": true, ".bin": true, ".pth": true, ".gguf": true}

func (m *ModelManager) ListModels(dir string) ([]ModelInfo, error) {
	return m.listFiles(dir, modelFileExts)
}

func (m *ModelManager) listFiles(dir string, exts map[string]bool) ([]ModelInfo, error) {
	if dir == "" {
		return nil, fmt.Errorf("directory not configured")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ModelInfo{}, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	var models []ModelInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !exts[ext] {
			continue
		}
		models = append(models, ModelInfo{
			Name:      entry.Name(),
			Size:      info.Size(),
			Extension: ext,
		})
	}
	return models, nil
}

func (m *ModelManager) DownloadModel(ctx context.Context, url, targetDir, filename, sha string) error {
	if targetDir == "" {
		return fmt.Errorf("directory not configured")
	}
	filename, err := safeFilename(filename)
	if err != nil {
		return err
	}
	want, err := parseSHA256(sha)
	if err != nil {
		return err
	}
	_, err = m.fetch(ctx, url, targetDir, filename, want, fetchEvents{})
	return err
}

func safeFilename(name string) (string, error) {
	base := filepath.Base(filepath.Clean(name))
	if name == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return "", fmt.Errorf("invalid filename %q", name)
	}
	return base, nil
}

func (m *ModelManager) DeleteModel(dir, filename string) error {
	if dir == "" {
		return fmt.Errorf("directory not configured")
	}
	filename, err := safeFilename(filename)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, filename)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete model: %w", err)
	}
	return nil
}

func (m *ModelManager) ollamaBinary() string {
	if pc, ok := m.config.Process("ollama"); ok && pc.Binary != "" {
		return pc.Binary
	}
	return "ollama"
}

func (m *ModelManager) ollamaModelsDir() string {
	return filepath.Join(m.config.DataDir, "models", "ollama")
}

func (m *ModelManager) ollamaEnv() []string {
	return append(os.Environ(), "OLLAMA_MODELS="+m.ollamaModelsDir())
}
