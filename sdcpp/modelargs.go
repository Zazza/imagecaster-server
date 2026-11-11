package sdcpp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var modelExts = []string{".safetensors", ".ckpt", ".gguf"}

func cleanName(name string) string {
	if i := strings.Index(name, " ["); i >= 0 {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

func hasModelExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range modelExts {
		if ext == e {
			return true
		}
	}
	return false
}

func stem(name string) string {
	base := filepath.Base(name)
	if hasModelExt(base) {
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return base
}

func lookupAlias(cfg Config, name string) ([]string, bool) {
	if a, ok := cfg.Aliases[name]; ok {
		return a, true
	}
	s := stem(name)
	for k, a := range cfg.Aliases {
		if stem(k) == s {
			return a, true
		}
	}
	return nil, false
}

func within(p string, dirs ...string) bool {
	p = filepath.Clean(p)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(d), p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func checkpointPath(cfg Config, name string) (string, error) {
	p, err := findCheckpoint(cfg, name)
	if err != nil {
		return "", err
	}
	if !within(p, cfg.ModelsDir) {
		return "", fmt.Errorf("checkpoint %q is outside %s", name, cfg.ModelsDir)
	}
	return p, nil
}

func findCheckpoint(cfg Config, name string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	if hasModelExt(name) {
		return filepath.Join(cfg.ModelsDir, name), nil
	}
	for _, ext := range modelExts {
		p := filepath.Join(cfg.ModelsDir, name+ext)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("checkpoint %q not found in %s", name, cfg.ModelsDir)
}

func ModelArgs(cfg Config, checkpoint string, modules []string) ([]string, error) {
	name := cleanName(checkpoint)
	if name == "" {
		return nil, fmt.Errorf("empty checkpoint")
	}
	if a, ok := lookupAlias(cfg, name); ok {
		return append([]string(nil), a...), nil
	}
	lower := strings.ToLower(stem(name))
	if strings.Contains(lower, "z-image") || strings.Contains(lower, "zimage") || strings.Contains(lower, "z_image") {
		return nil, fmt.Errorf("model %q: sd.cpp cannot load the Forge all-in-one Z-Image file; add an alias with a GGUF diffusion model, text encoder and VAE", name)
	}
	path, err := checkpointPath(cfg, name)
	if err != nil {
		return nil, err
	}
	klein := strings.Contains(lower, "klein")
	moss := strings.Contains(lower, "moss")
	if !klein && !moss {
		return []string{"--model", path}, nil
	}
	var vae, llm string
	for _, m := range modules {
		mp := m
		if !filepath.IsAbs(mp) {
			if cfg.VAEDir != "" && fileExists(filepath.Join(cfg.VAEDir, mp)) {
				mp = filepath.Join(cfg.VAEDir, mp)
			} else if cfg.TextEncoderDir != "" {
				mp = filepath.Join(cfg.TextEncoderDir, mp)
			}
		}
		if !within(mp, cfg.VAEDir, cfg.TextEncoderDir, cfg.ModelsDir) {
			return nil, fmt.Errorf("module %q is outside the VAE, text encoder and model folders", m)
		}
		if isVAEModule(cfg, mp) {
			vae = mp
		} else {
			llm = mp
		}
	}
	if vae == "" || llm == "" {
		return nil, fmt.Errorf("model %q needs a VAE and a text encoder in forge_additional_modules, got %v", name, modules)
	}
	args := []string{"--diffusion-model", path, "--llm", llm, "--vae", vae}
	if moss {
		args = append(args, "--tokenizer", filepath.Join(cfg.TokenizerDir, "moss.tokenizer.json"))
	}
	return args, nil
}

func isVAEModule(cfg Config, p string) bool {
	if cfg.VAEDir != "" {
		if rel, err := filepath.Rel(cfg.VAEDir, p); err == nil && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(filepath.Base(p)), "vae")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func usesSingleFile(args []string) bool {
	return len(args) > 0 && args[0] == "--model"
}
