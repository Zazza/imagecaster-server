package sdcpp

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var loraTag = regexp.MustCompile(`<lora:([^:>]+)(?::([^:>]*))?(?::[^>]*)?>`)

type loraEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

var loraExts = map[string]bool{".gguf": true, ".pt": true, ".pth": true, ".safetensors": true, ".ckpt": true, ".sft": true}

func scanLoras(dir string) []loraEntry {
	out := []loraEntry{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() && loraExts[strings.ToLower(filepath.Ext(p))] {
			if rel, err := filepath.Rel(dir, p); err == nil {
				out = append(out, loraEntry{Name: strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())), Path: filepath.ToSlash(rel)})
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (f *Facade) fetchLoras(ctx context.Context) ([]loraEntry, error) {
	if f.cfg.LoraDir != "" {
		return scanLoras(f.cfg.LoraDir), nil
	}
	code, body, err := f.do(ctx, http.MethodGet, "/sdcpp/v1/capabilities", nil, 30*time.Second)
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable: %v", err)
	}
	if code != http.StatusOK {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server capabilities HTTP %d: %s", code, truncate(body, 200))
	}
	var caps struct {
		Loras []loraEntry `json:"loras"`
	}
	if err := json.Unmarshal(body, &caps); err != nil {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "decode capabilities: %v", err)
	}
	if caps.Loras == nil {
		caps.Loras = []loraEntry{}
	}
	return caps.Loras, nil
}

func checkLoraWeights(prompt string) error {
	for _, m := range loraTag.FindAllStringSubmatch(prompt, -1) {
		if s := strings.TrimSpace(m[2]); s != "" {
			if _, err := strconv.ParseFloat(s, 64); err != nil {
				return apiErr(http.StatusBadRequest, "invalid_params", "LoRA %q: bad weight %q", strings.TrimSpace(m[1]), s)
			}
		}
	}
	return nil
}

func (f *Facade) extractLoras(ctx context.Context, prompt string) (string, []map[string]any, error) {
	matches := loraTag.FindAllStringSubmatch(prompt, -1)
	out := []map[string]any{}
	if len(matches) == 0 {
		return prompt, out, nil
	}
	if err := checkLoraWeights(prompt); err != nil {
		return "", nil, err
	}
	list, err := f.fetchLoras(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		mult := 1.0
		if s := strings.TrimSpace(m[2]); s != "" {
			mult, _ = strconv.ParseFloat(s, 64)
		}
		path := ""
		for _, l := range list {
			if l.Name == name || stem(l.Path) == name || l.Name == stem(name) {
				path = l.Path
				break
			}
		}
		if path == "" {
			log.Printf("[sdcpp] LoRA %q not found, skipped", name)
			continue
		}
		out = append(out, map[string]any{"path": path, "multiplier": mult})
	}
	clean := strings.Join(strings.Fields(loraTag.ReplaceAllString(prompt, " ")), " ")
	return clean, out, nil
}
