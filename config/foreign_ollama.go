package config

import (
	"os"
	"path/filepath"
	"runtime"
)

func OllamaDirCandidates(goos string, getenv func(string) string, home string) []string {
	var out []string
	if d := getenv("OLLAMA_MODELS"); d != "" {
		out = append(out, d)
	} else if home != "" {
		out = append(out, filepath.Join(home, ".ollama", "models"))
	}
	if goos == "linux" {
		out = append(out, "/usr/share/ollama/.ollama/models")
	}
	return out
}

func FilterOllamaDirs(candidates []string, own string) []string {
	seen := map[string]bool{canonicalDir(own): true}
	var out []string
	for _, d := range candidates {
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			continue
		}
		key := canonicalDir(d)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, filepath.Clean(d))
	}
	return out
}

func canonicalDir(d string) string {
	abs, err := filepath.Abs(d)
	if err != nil {
		abs = d
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(abs)
}

func (c *Config) ForeignOllamaDirs() []string {
	if c.LLM.ReadOllama != nil && !*c.LLM.ReadOllama {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return FilterOllamaDirs(OllamaDirCandidates(runtime.GOOS, os.Getenv, home), c.LLMModelsDir())
}
