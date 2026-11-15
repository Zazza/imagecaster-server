package main

import (
	"imagecaster-server/config"
	"imagecaster-server/fsutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const configName = "server-config.yaml"

func absDir(dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[1:])
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func firstRunConfig(cfgFile, dataDir string) (*config.Config, error) {
	cfg := config.NewDefault()
	cfg.DataDir = dataDir
	cfg.ApplyBackendToProcess()
	cfg.ApplyInstallDefaults()
	cfg.ResolvePaths()
	if err := saveConfig(cfgFile, &cfg); err != nil {
		return nil, err
	}
	if err := config.SaveDataDir(dataDir); err != nil {
		log.Printf("data dir not remembered: %v", err)
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := []byte("# Imagecaster Server Configuration\n\n")
	return fsutil.ReplaceFile(path, append(header, data...), 0o644)
}
