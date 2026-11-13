package installer

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	"imagecaster-server/config"
)

// Перенос старых каталогов движков без суффикса платформы в новые.
// Папка данных может быть общим томом у Windows и Linux: переносится только
// каталог с бинарником текущей ОС, сборка чужой ОС остаётся на месте.
func (inst *Installer) MigrateEngineDirs() {
	moves := []struct{ name, legacy, next, probe string }{
		{"llama.cpp", config.LlamaCppDirLegacy, config.LlamaCppDir, "llama-server" + exeSuffixFor(runtime.GOOS)},
		{"sd.cpp", config.SDCppDirLegacy, config.SDCppDir, "sd-server" + exeSuffixFor(runtime.GOOS)},
	}
	targets := map[string]bool{}
	for _, bc := range inst.config.Backends {
		if bc.Install.Target != "" {
			targets[filepath.Clean(inst.resolveTarget(bc.Install.Target))] = true
		}
	}
	for _, pc := range inst.config.Processes {
		if pc.Install.Target != "" {
			targets[filepath.Clean(inst.resolveTarget(pc.Install.Target))] = true
		}
	}
	for _, m := range moves {
		legacy := filepath.Join(inst.config.DataDir, filepath.FromSlash(m.legacy))
		next := filepath.Join(inst.config.DataDir, filepath.FromSlash(m.next))
		if !targets[next] || !dirExists(legacy) || dirExists(next) {
			continue
		}
		if _, err := os.Stat(filepath.Join(legacy, m.probe)); err != nil {
			log.Printf("[install] %s: old %s has no %s for this OS, left as is", m.name, legacy, m.probe)
			continue
		}
		if err := os.Rename(legacy, next); err != nil {
			log.Printf("[install] %s: move %s to %s failed: %v (will reinstall)", m.name, legacy, next, err)
			continue
		}
		log.Printf("[install] %s: moved %s to %s", m.name, legacy, next)
	}
}

func exeSuffixFor(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
