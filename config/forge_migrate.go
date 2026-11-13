package config

import (
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ForgeRemovalBackupSuffix = ".bak-before-forge-removal"
	ForgeCleanupBackupSuffix = ".bak-before-forge-cleanup"
)

type droppedBackend struct {
	key                        string
	modelsDir, loraDir, vaeDir string
}

func mapNode(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func activeSDOf(data []byte) string {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return ""
	}
	return mapValue(doc.Content[0], "active_sd")
}

func dropForgeBackends(path string, data []byte, dataDir, origActive string) []byte {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data
	}
	root := doc.Content[0]
	if d := mapValue(root, "data_dir"); d != "" {
		dataDir = d
	}
	active := mapValue(root, "active_sd")
	backends := mapNode(root, "backends")
	var dropped []droppedBackend
	kept := map[string]bool{}
	if backends != nil && backends.Kind == yaml.MappingNode {
		content := make([]*yaml.Node, 0, len(backends.Content))
		for i := 0; i+1 < len(backends.Content); i += 2 {
			k, v := backends.Content[i], backends.Content[i+1]
			if k.Value != SDCppBackendKey && v.Kind == yaml.MappingNode && mapValue(v, "kind") != KindSDCpp {
				dropped = append(dropped, droppedBackend{key: k.Value, modelsDir: mapValue(v, "models_dir"), loraDir: mapValue(v, "lora_dir"), vaeDir: mapValue(v, "vae_dir")})
				continue
			}
			kept[k.Value] = true
			content = append(content, k, v)
		}
		backends.Content = content
	}
	switchActive := active != "" && active != SDCppBackendKey && !kept[active]
	if switchActive {
		setMapValue(root, "active_sd", SDCppBackendKey)
	}
	prefer := active
	if prefer == "" || prefer == SDCppBackendKey {
		prefer = origActive
	}
	adopted := ""
	if !kept[SDCppBackendKey] {
		slices.SortStableFunc(dropped, func(a, b droppedBackend) int {
			switch {
			case a.key == prefer && b.key != prefer:
				return -1
			case b.key == prefer && a.key != prefer:
				return 1
			}
			return strings.Compare(a.key, b.key)
		})
		candidates := append(slices.Clone(dropped), droppedBackend{key: "forge-neo", modelsDir: "sd-webui-forge-neo/models/Stable-diffusion"})
		for _, d := range candidates {
			if d.modelsDir == "" || !dirExists(resolveDirPath(dataDir, d.modelsDir)) {
				continue
			}
			n := adoptedSDCppNode(d)
			if n == nil {
				break
			}
			if backends == nil || backends.Kind != yaml.MappingNode {
				backends = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				setMapNode(root, "backends", backends)
			}
			backends.Content = append(backends.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: SDCppBackendKey}, n)
			adopted = d.modelsDir
			break
		}
	}
	removed := len(dropped) > 0 || switchActive || adopted != ""
	cleaned := cleanForgeProcesses(root, backends)
	if !removed && len(cleaned) == 0 {
		return data
	}
	out, err := encodeYAML(&doc)
	if err != nil {
		return data
	}
	suffix := ForgeCleanupBackupSuffix
	msg := "[config] Forge leftovers removed from processes [" + strings.Join(cleaned, ", ") + "]"
	if removed {
		suffix = ForgeRemovalBackupSuffix
		names := make([]string, 0, len(dropped))
		for _, d := range dropped {
			names = append(names, d.key)
		}
		msg = "[config] Forge support removed: backends dropped [" + strings.Join(names, ", ") + "], active_sd: sdcpp"
		if adopted != "" {
			msg += ", sd.cpp uses models from " + adopted
		}
		if len(cleaned) > 0 {
			msg += ", processes cleaned [" + strings.Join(cleaned, ", ") + "]"
		}
	}
	if err := writeMigrated(path, suffix, data, out); err != nil {
		log.Printf("%s; config not saved: %v", msg, err)
		return out
	}
	log.Printf("%s; old config saved as %s", msg, path+suffix)
	return out
}

var forgeEnvKeys = []string{"PYTORCH_CUDA_ALLOC_CONF", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "TORCH_COMMAND", "VIRTUAL_ENV", "UV_CACHE_DIR", "UV_PYTHON_INSTALL_DIR"}

func cleanForgeProcesses(root, backends *yaml.Node) []string {
	procs := mapNode(root, "processes")
	if procs == nil || procs.Kind != yaml.MappingNode {
		return nil
	}
	var cleaned []string
	if deleteMapKey(procs, "python") {
		cleaned = append(cleaned, "python")
	}
	backend, ok := activeSDCppBackend(root, backends)
	if !ok || backend.ProcessKey == "" || backend.ProcessKey == LlamaCppProcessKey || backend.ProcessKey == "ollama" {
		return cleaned
	}
	node := mapNode(procs, backend.ProcessKey)
	if node == nil || node.Kind != yaml.MappingNode {
		return cleaned
	}
	var pc ProcessConfig
	if err := node.Decode(&pc); err != nil || !hasForgeLeftovers(pc) {
		return cleaned
	}
	setMapValue(node, "binary", backend.Binary)
	setMapValue(node, "workdir", backend.WorkDir)
	setMapValue(node, "health_url", sdHealthURL(pc.HealthURL))
	args := withDirFlag(backend.Args, "--lora-model-dir", backend.LoraDir)
	args = withDirFlag(args, "--hires-upscalers-dir", backend.UpscalerDir)
	var seq yaml.Node
	if err := seq.Encode(sdcppArgsOnPort(args, targetPort(pc.TargetURL, 7860))); err == nil {
		setMapNode(node, "args", &seq)
	}
	if env := mapNode(node, "env"); env != nil && env.Kind == yaml.MappingNode {
		for _, k := range forgeEnvKeys {
			deleteMapKey(env, k)
		}
		if len(env.Content) == 0 {
			deleteMapKey(node, "env")
		}
	}
	if pc.Install.Method != "" && pc.Install.Method != InstallRelease {
		deleteMapKey(node, "install")
	}
	return append(cleaned, backend.ProcessKey)
}

func activeSDCppBackend(root, backends *yaml.Node) (BackendConfig, bool) {
	active := mapValue(root, "active_sd")
	if backends != nil && backends.Kind == yaml.MappingNode {
		if n := mapNode(backends, active); n != nil {
			var b BackendConfig
			if err := n.Decode(&b); err != nil {
				return BackendConfig{}, false
			}
			return b, active == SDCppBackendKey || b.Kind == KindSDCpp
		}
	}
	if active == SDCppBackendKey {
		return sdcppBackend(), true
	}
	return BackendConfig{}, false
}

func hasForgeLeftovers(pc ProcessConfig) bool {
	if strings.HasSuffix(pc.HealthURL, legacyHealthPath) {
		return true
	}
	if pc.Install.Method != "" && pc.Install.Method != InstallRelease {
		return true
	}
	if strings.HasPrefix(strings.ToLower(filepath.Base(pc.Binary)), "python") || strings.Contains(strings.ToLower(pc.WorkDir), "forge") {
		return true
	}
	for _, a := range pc.Args {
		if strings.HasSuffix(a, ".py") {
			return true
		}
	}
	for _, k := range forgeEnvKeys {
		if _, ok := pc.Env[k]; ok {
			return true
		}
	}
	return false
}

func deleteMapKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

func setMapNode(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func adoptedSDCppNode(d droppedBackend) *yaml.Node {
	sd := sdcppBackend()
	root := filepath.Dir(d.modelsDir)
	sd.ModelsDir = d.modelsDir
	sd.LoraDir = orDefault(d.loraDir, filepath.Join(root, "Lora"))
	sd.VaeDir = orDefault(d.vaeDir, filepath.Join(root, "VAE"))
	sd.TextEncoderDir = filepath.Join(root, "text_encoder")
	sd.TokenizerDir = sd.TextEncoderDir
	sd.PoseModelsDir = filepath.Join(root, "ControlNetPreprocessor", "openpose")
	sd.UpscalerDir = filepath.Join(root, "ESRGAN")
	var n yaml.Node
	if err := n.Encode(sd); err != nil {
		return nil
	}
	return &n
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
