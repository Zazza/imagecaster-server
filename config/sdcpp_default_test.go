package config

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sddOwnDirs = map[string]string{
	"ModelsDir":      "models/checkpoints",
	"TextEncoderDir": "models/text_encoders",
	"VaeDir":         "models/vae",
	"LoraDir":        "models/loras",
	"UpscalerDir":    "models/upscalers",
	"PoseModelsDir":  "models/openpose",
}

func sddDirs(b BackendConfig) map[string]string {
	return map[string]string{
		"ModelsDir":      b.ModelsDir,
		"TextEncoderDir": b.TextEncoderDir,
		"VaeDir":         b.VaeDir,
		"LoraDir":        b.LoraDir,
		"UpscalerDir":    b.UpscalerDir,
		"PoseModelsDir":  b.PoseModelsDir,
	}
}

func sddOnnxName() string {
	switch runtime.GOOS {
	case "windows":
		return "onnxruntime.dll"
	case "darwin":
		return "libonnxruntime.1.31.1.dylib"
	}
	return "libonnxruntime.so.1.31.1"
}

func sddBinary() string {
	if runtime.GOOS == "windows" {
		return SDCppDir + "/sd-server.exe"
	}
	return SDCppDir + "/sd-server"
}

func sddIsolateEnv(t *testing.T) {
	t.Setenv("SD_ACTIVE_BACKEND", "")
	t.Setenv("SD_DATA_DIR", "")
}

func TestNewDefaultActiveIsSDCpp(t *testing.T) {
	cfg := NewDefault()

	assert.Equal(t, "sdcpp", SDCppBackendKey)
	assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
	require.Contains(t, cfg.Backends, SDCppBackendKey)
	b := cfg.Backends[SDCppBackendKey]
	assert.Equal(t, KindSDCpp, b.Kind)
	assert.Equal(t, "sd", b.ProcessKey)
	assert.Equal(t, InstallMethod("release"), InstallRelease)
	assert.Equal(t, InstallRelease, b.Install.Method)
	assert.Equal(t, SDCppDir, b.Install.Target)
	assert.NotEmpty(t, SDCppVersion)
	assert.Equal(t, SDCppVersion, b.Install.Version)
}

func TestNewDefaultSDCppRelativePaths(t *testing.T) {
	b := NewDefault().Backends[SDCppBackendKey]
	got := sddDirs(b)
	for field, want := range sddOwnDirs {
		t.Run(field, func(t *testing.T) {
			assert.Equal(t, want, got[field])
			assert.False(t, filepath.IsAbs(got[field]))
		})
	}
	t.Run("Binary", func(t *testing.T) {
		assert.Equal(t, sddBinary(), b.Binary)
	})
	t.Run("OnnxRuntimeLib", func(t *testing.T) {
		lib := filepath.ToSlash(b.OnnxRuntimeLib)
		assert.True(t, strings.HasPrefix(lib, "engines/onnxruntime/"), lib)
		assert.Equal(t, sddOnnxName(), path.Base(lib))
	})
}

func TestNewDefaultKeepsForgeNeoForRollback(t *testing.T) {
	cfg := NewDefault()
	require.Contains(t, cfg.Backends, NeoBackendKey)
	neo := cfg.Backends[NeoBackendKey]
	assert.Equal(t, "Stable Diffusion Forge Neo", neo.Name)
	assert.Equal(t, "sd", neo.ProcessKey)
	assert.Equal(t, InstallZip, neo.Install.Method)
	assert.Equal(t, NeoVersion, neo.Install.Version)
	assert.Equal(t, "sd-webui-forge-neo/models/Stable-diffusion", neo.ModelsDir)
	assert.Equal(t, "sd-webui-forge-neo/models/Lora", neo.LoraDir)
	assert.Equal(t, "sd-webui-forge-neo/models/VAE", neo.VaeDir)
	assert.NotEqual(t, KindSDCpp, neo.Kind)
}

func TestResolvePathsSDCppInsideDataDir(t *testing.T) {
	cfg := NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.ResolvePaths()
	b := cfg.Backends[SDCppBackendKey]

	got := sddDirs(b)
	got["Binary"] = b.Binary
	got["Install.Target"] = b.Install.Target
	want := map[string]string{
		"Binary":         sddBinary(),
		"Install.Target": SDCppDir,
	}
	for k, v := range sddOwnDirs {
		want[k] = v
	}
	for field, rel := range want {
		t.Run(field, func(t *testing.T) {
			assert.Equal(t, filepath.Join(cfg.DataDir, filepath.FromSlash(rel)), got[field])
		})
	}
	t.Run("OnnxRuntimeLib", func(t *testing.T) {
		assert.True(t, filepath.IsAbs(b.OnnxRuntimeLib))
		assert.True(t, strings.HasPrefix(b.OnnxRuntimeLib, filepath.Join(cfg.DataDir, "engines", "onnxruntime")+string(filepath.Separator)), b.OnnxRuntimeLib)
		assert.Equal(t, sddOnnxName(), filepath.Base(b.OnnxRuntimeLib))
	})
}

func TestLoadNewInstallDefaultsToSDCpp(t *testing.T) {
	sddIsolateEnv(t)
	dataDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	cfg, err := LoadWithDir(cfgPath, dataDir)
	require.NoError(t, err)
	assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
	require.FileExists(t, cfgPath)

	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sd-webui-forge-neo", "models", "Stable-diffusion"), 0o755))

	for lname, load := range loaders(dataDir) {
		t.Run(lname, func(t *testing.T) {
			again, err := load(cfgPath)
			require.NoError(t, err)
			assert.Equal(t, SDCppBackendKey, again.ActiveSD)
			assert.Contains(t, again.Backends, NeoBackendKey)
			got := sddDirs(again.Backends[SDCppBackendKey])
			for field, rel := range sddOwnDirs {
				assert.Equal(t, filepath.Join(dataDir, filepath.FromSlash(rel)), got[field], field)
			}
		})
	}
}

func sddForgeDirs(modelsDir, lora, vae string) map[string]string {
	parent := filepath.Dir(modelsDir)
	return map[string]string{
		"ModelsDir":      modelsDir,
		"LoraDir":        lora,
		"VaeDir":         vae,
		"TextEncoderDir": filepath.Join(parent, "text_encoder"),
		"PoseModelsDir":  filepath.Join(parent, "ControlNetPreprocessor", "openpose"),
		"UpscalerDir":    filepath.Join(parent, "ESRGAN"),
	}
}

func TestLoadOldConfigWithoutSDCppSection(t *testing.T) {
	tests := []struct {
		name       string
		yaml       func(dataDir, forgeRoot string) string
		prepare    func(t *testing.T, dataDir, forgeRoot string)
		wantActive string
		want       func(dataDir, forgeRoot string) map[string]string
	}{
		{
			name: "forge-neo default folder exists",
			yaml: func(dataDir, _ string) string {
				return "active_sd: forge-neo\n"
			},
			prepare: func(t *testing.T, dataDir, _ string) {
				sddTouch(t, filepath.Join(dataDir, "sd-webui-forge-neo", "models", "Stable-diffusion", "model.safetensors"))
			},
			wantActive: SDCppBackendKey,
			want: func(dataDir, _ string) map[string]string {
				m := filepath.Join(dataDir, "sd-webui-forge-neo", "models")
				return sddForgeDirs(filepath.Join(m, "Stable-diffusion"), filepath.Join(m, "Lora"), filepath.Join(m, "VAE"))
			},
		},
		{
			name: "forge-neo section with own folders",
			yaml: func(_, forgeRoot string) string {
				r := filepath.ToSlash(forgeRoot)
				return "active_sd: forge-neo\nbackends:\n  forge-neo:\n    name: Neo\n    process_key: sd\n    binary: " + r + "/venv/bin/python\n    models_dir: " + r + "/models/Stable-diffusion\n    lora_dir: " + r + "/models/Lora\n    vae_dir: " + r + "/models/VAE\n"
			},
			prepare: func(t *testing.T, _, forgeRoot string) {
				sddTouch(t, filepath.Join(forgeRoot, "models", "Stable-diffusion", "model.safetensors"))
			},
			wantActive: SDCppBackendKey,
			want: func(_, forgeRoot string) map[string]string {
				m := filepath.Join(forgeRoot, "models")
				return sddForgeDirs(filepath.Join(m, "Stable-diffusion"), filepath.Join(m, "Lora"), filepath.Join(m, "VAE"))
			},
		},
		{
			name: "active custom forge backend",
			yaml: func(_, forgeRoot string) string {
				r := filepath.ToSlash(forgeRoot)
				return "active_sd: my-forge\nbackends:\n  my-forge:\n    name: My Forge\n    process_key: sd\n    binary: " + r + "/venv/bin/python\n    models_dir: " + r + "/m/ckpt\n    lora_dir: " + r + "/l\n    vae_dir: " + r + "/v\n"
			},
			prepare: func(t *testing.T, dataDir, forgeRoot string) {
				sddTouch(t, filepath.Join(forgeRoot, "m", "ckpt", "model.safetensors"))
				sddTouch(t, filepath.Join(dataDir, "sd-webui-forge-neo", "models", "Stable-diffusion", "other.safetensors"))
			},
			wantActive: SDCppBackendKey,
			want: func(_, forgeRoot string) map[string]string {
				return sddForgeDirs(filepath.Join(forgeRoot, "m", "ckpt"), filepath.Join(forgeRoot, "l"), filepath.Join(forgeRoot, "v"))
			},
		},
		{
			name: "forge folder absent",
			yaml: func(dataDir, _ string) string {
				return "active_sd: forge-neo\n"
			},
			prepare:    func(t *testing.T, _, _ string) {},
			wantActive: SDCppBackendKey,
			want: func(dataDir, _ string) map[string]string {
				m := map[string]string{}
				for k, rel := range sddOwnDirs {
					m[k] = filepath.Join(dataDir, filepath.FromSlash(rel))
				}
				return m
			},
		},
	}
	for _, tt := range tests {
		for lname, load := range loaders(t.TempDir()) {
			t.Run(tt.name+"/"+lname, func(t *testing.T) {
				sddIsolateEnv(t)
				dataDir := t.TempDir()
				forgeRoot := t.TempDir()
				tt.prepare(t, dataDir, forgeRoot)
				body := "data_dir: " + filepath.ToSlash(dataDir) + "\n" + legacyProcesses + tt.yaml(dataDir, forgeRoot)

				cfg, err := load(writeYAML(t, body))
				require.NoError(t, err)

				assert.Equal(t, tt.wantActive, cfg.ActiveSD)
				require.Contains(t, cfg.Backends, SDCppBackendKey)
				sd := cfg.Backends[SDCppBackendKey]
				assert.Equal(t, KindSDCpp, sd.Kind)
				assert.Equal(t, InstallRelease, sd.Install.Method)
				got := sddDirs(sd)
				for field, want := range tt.want(dataDir, forgeRoot) {
					assert.Equal(t, want, got[field], field)
				}
				ownModels := filepath.Join(dataDir, "models")
				_ = filepath.Walk(ownModels, func(p string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() {
						t.Errorf("file appeared in own models dir: %s", p)
					}
					return nil
				})
			})
		}
	}
}

func TestLoadKeepsExplicitSDCppSection(t *testing.T) {
	for lname, load := range loaders(t.TempDir()) {
		t.Run(lname, func(t *testing.T) {
			sddIsolateEnv(t)
			dataDir := t.TempDir()
			mine := filepath.ToSlash(t.TempDir())
			sddTouch(t, filepath.Join(dataDir, "sd-webui-forge-neo", "models", "Stable-diffusion", "model.safetensors"))
			body := "data_dir: " + filepath.ToSlash(dataDir) + "\n" + legacyProcesses +
				"active_sd: forge-neo\nbackends:\n  sdcpp:\n    name: sd.cpp\n    kind: sdcpp\n    process_key: sd\n    binary: engines/sd-cpp/sd-server\n" +
				"    models_dir: " + mine + "/ckpt\n    lora_dir: " + mine + "/lora\n    text_encoder_dir: " + mine + "/te\n"

			cfg, err := load(writeYAML(t, body))
			require.NoError(t, err)

			assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
			sd := cfg.Backends[SDCppBackendKey]
			assert.Equal(t, filepath.FromSlash(mine+"/ckpt"), sd.ModelsDir)
			assert.Equal(t, filepath.FromSlash(mine+"/lora"), sd.LoraDir)
			assert.Equal(t, filepath.FromSlash(mine+"/te"), sd.TextEncoderDir)
			forgeModels := filepath.Join(dataDir, "sd-webui-forge-neo", "models")
			assert.NotEqual(t, filepath.Join(forgeModels, "VAE"), sd.VaeDir)
			assert.NotEqual(t, filepath.Join(forgeModels, "text_encoder"), sd.TextEncoderDir)
			assert.NotEqual(t, filepath.Join(forgeModels, "ESRGAN"), sd.UpscalerDir)
		})
	}
}

func sddTouch(t *testing.T, p string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
}

func TestLoadSDCppReleaseVersionComesFromServer(t *testing.T) {
	tests := []struct {
		name        string
		binary      string
		install     string
		wantMethod  InstallMethod
		wantVersion string
	}{
		{name: "old release version", binary: "engines/sd-cpp/sd-server", install: "    install:\n      method: release\n      target: engines/sd-cpp\n      version: master-1000-oldsha\n", wantMethod: InstallRelease, wantVersion: SDCppVersion},
		{name: "empty release version", binary: "engines/sd-cpp/sd-server", install: "    install:\n      method: release\n      target: engines/sd-cpp\n      version: \"\"\n", wantMethod: InstallRelease, wantVersion: SDCppVersion},
		{name: "manual method keeps version", binary: "/opt/sd/sd-server", install: "    install:\n      method: manual\n      version: custom-1\n", wantMethod: InstallMethod("manual"), wantVersion: "custom-1"},
		{name: "empty install with own binary", binary: "/opt/sd/sd-server", install: "    install: {}\n", wantMethod: "", wantVersion: ""},
	}
	for _, tt := range tests {
		for lname, load := range loaders(t.TempDir()) {
			t.Run(tt.name+"/"+lname, func(t *testing.T) {
				sddIsolateEnv(t)
				dataDir := t.TempDir()
				mine := filepath.ToSlash(t.TempDir())
				body := "data_dir: " + filepath.ToSlash(dataDir) + "\n" + legacyProcesses +
					"active_sd: sdcpp\nbackends:\n  sdcpp:\n    name: My sd.cpp\n    kind: sdcpp\n    process_key: sd\n    binary: " + tt.binary + "\n" +
					"    models_dir: " + mine + "/ckpt\n    lora_dir: " + mine + "/lora\n" + tt.install

				cfg, err := load(writeYAML(t, body))
				require.NoError(t, err)

				sd := cfg.Backends[SDCppBackendKey]
				assert.Equal(t, tt.wantMethod, sd.Install.Method)
				assert.Equal(t, tt.wantVersion, sd.Install.Version)
				assert.Equal(t, "My sd.cpp", sd.Name)
				assert.Equal(t, filepath.FromSlash(mine+"/ckpt"), sd.ModelsDir)
				assert.Equal(t, filepath.FromSlash(mine+"/lora"), sd.LoraDir)
				if filepath.IsAbs(tt.binary) {
					assert.Equal(t, tt.binary, sd.Binary)
				} else {
					assert.Equal(t, filepath.Join(dataDir, filepath.FromSlash(tt.binary)), sd.Binary)
				}
			})
		}
	}
}
