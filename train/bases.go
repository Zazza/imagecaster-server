package train

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	baseZImage = "Z-Image"
	baseMoss   = "Moss-Image"
	baseSDXL   = "SDXL"
	baseFlux2  = "Flux.2"

	fileQwen3TE  = "qwen_3_4b.safetensors"
	fileKleinVAE = "flux2-klein-vae.safetensors"
)

const (
	dirModels = iota
	dirTextEncoder
	dirVAE
	dirTrain
)

type need struct {
	dir   int
	names []string
}

type localFiles struct {
	TE  string `json:"te,omitempty"`
	VAE string `json:"vae,omitempty"`
}

type baseSpec struct {
	name  string
	needs []need
	build func(p []string) (modelBlock, localFiles)
}

var trainBases = []baseSpec{
	{
		name: baseZImage,
		needs: []need{
			{dirTrain, []string{"z_image_turbo_bf16.safetensors"}},
			{dirTrain, []string{"zimage_turbo_training_adapter_v2.safetensors"}},
			{dirTextEncoder, []string{fileQwen3TE}},
			{dirVAE, []string{"ae.safetensors"}},
		},
		build: func(p []string) (modelBlock, localFiles) {
			return zimageBlock(p[0], p[1]), localFiles{TE: p[2], VAE: p[3]}
		},
	},
	{
		name: baseMoss,
		needs: []need{
			{dirModels, []string{"moss_image_sft_bf16.safetensors"}},
			{dirTextEncoder, []string{"qwen35_9b_int8_convrot.safetensors"}},
			{dirVAE, []string{fileKleinVAE, "flux2-vae.safetensors", "flux2-vae_small.safetensors"}},
		},
		build: func(p []string) (modelBlock, localFiles) {
			return mossBlock(p[0], p[1], p[2]), localFiles{}
		},
	},
	{
		name: baseSDXL,
		needs: []need{
			{dirModels, []string{"juggernautXL_ragnarokBy.safetensors", "epicrealismXL_pureFix.safetensors"}},
		},
		build: func(p []string) (modelBlock, localFiles) {
			return sdxlBlock(p[0]), localFiles{}
		},
	},
	{
		name: baseFlux2,
		needs: []need{
			{dirModels, []string{"flux-2-klein-4b.safetensors"}},
			{dirTextEncoder, []string{fileQwen3TE}},
			{dirVAE, []string{fileKleinVAE}},
		},
		build: func(p []string) (modelBlock, localFiles) {
			return kleinBlock(p[0], p[2]), localFiles{TE: p[1]}
		},
	},
}

func knownBase(name string) bool {
	for _, b := range trainBases {
		if b.name == name {
			return true
		}
	}
	return false
}

func (m *Manager) dir(d int) string {
	switch d {
	case dirModels:
		return m.cfg.ModelsDir
	case dirTextEncoder:
		return m.cfg.TextEncoderDir
	case dirVAE:
		return m.cfg.VAEDir
	case dirTrain:
		return m.cfg.TrainDir
	}
	return ""
}

func existingFile(dir, name string) (string, bool) {
	if dir == "" {
		return "", false
	}
	p, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

func (m *Manager) files(b baseSpec) ([]string, bool) {
	out := make([]string, 0, len(b.needs))
	for _, n := range b.needs {
		found := ""
		for _, name := range n.names {
			if p, ok := existingFile(m.dir(n.dir), name); ok {
				found = p
				break
			}
		}
		if found == "" {
			return nil, false
		}
		out = append(out, found)
	}
	return out, true
}

func (m *Manager) baseAvailable(name string) bool {
	for _, b := range trainBases {
		if b.name == name {
			_, ok := m.files(b)
			return ok
		}
	}
	return false
}

func (m *Manager) Bases() []string {
	out := []string{}
	for _, b := range trainBases {
		if _, ok := m.files(b); ok {
			out = append(out, b.name)
		}
	}
	return out
}

func (m *Manager) prepareBase(name string, spec RunSpec, jobName, trigger string) error {
	for _, b := range trainBases {
		if b.name != name {
			continue
		}
		p, ok := m.files(b)
		if !ok {
			return &codedError{code: "base_unavailable", err: errors.New(name + " model files are missing on the server")}
		}
		block, local := b.build(p)
		if err := writeBaseConfig(spec, jobName, trigger, block); err != nil {
			return &codedError{code: "config_failed", err: err}
		}
		data, err := json.Marshal(local)
		if err != nil {
			return &codedError{code: "config_failed", err: err}
		}
		if err := os.WriteFile(spec.LocalPath, data, 0o644); err != nil {
			return &codedError{code: "config_failed", err: err}
		}
		return nil
	}
	return &codedError{code: "base_unavailable", err: errors.New("unknown training model " + name)}
}
