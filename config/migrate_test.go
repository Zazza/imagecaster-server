package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOldConfigMigration(t *testing.T) {
	cases := []struct {
		name        string
		activeSD    string
		backend     string
		modelsDir   string
		wantModels  string
		wantMigrate bool
	}{
		{"neo install", "forge-neo", "forge-neo", "sd-webui-forge-neo/models/Stable-diffusion", "sd-webui-forge-neo/models/Stable-diffusion", true},
		{"legacy forge key", "forge", "forge", "stable-diffusion-webui-forge/models/Stable-diffusion", "stable-diffusion-webui-forge/models/Stable-diffusion", true},
		{"custom backend", "my-sd", "my-sd", "mine/models/Stable-diffusion", "mine/models/Stable-diffusion", true},
		{"no forge models", "forge-neo", "forge-neo", "", "models/checkpoints", true},
	}
	for _, tc := range cases {
		for lname, load := range loaders("") {
			t.Run(tc.name+"/"+lname, func(t *testing.T) {
				dataDir := t.TempDir()
				body := "data_dir: " + filepath.ToSlash(dataDir) + "\n# keep me\nactive_sd: " + tc.activeSD + "\n" + legacyProcesses
				if tc.modelsDir != "" {
					require.NoError(t, os.MkdirAll(filepath.Join(dataDir, tc.modelsDir), 0o755))
					body += "backends:\n  " + tc.backend + ":\n    name: Old\n    process_key: sd\n    models_dir: " + tc.modelsDir + "\n"
				}
				path := filepath.Join(t.TempDir(), "server-config.yaml")
				require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

				cfg, err := load(path)
				require.NoError(t, err)
				assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
				assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)
				assert.Equal(t, filepath.Join(dataDir, tc.wantModels), cfg.GetActiveBackend().ModelsDir)

				backup, err := os.ReadFile(path + EngineMigrationBackupSuffix)
				require.NoError(t, err)
				assert.Equal(t, body, string(backup))
				saved, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Contains(t, string(saved), "active_sd: sdcpp")
				assert.Contains(t, string(saved), "active_llm: llamacpp")
				assert.Contains(t, string(saved), "# keep me")

				again, err := load(path)
				require.NoError(t, err)
				assert.Equal(t, SDCppBackendKey, again.ActiveSD)
				assert.Equal(t, LLMLlamaCpp, again.ActiveLLM)
				assert.Equal(t, filepath.Join(dataDir, tc.wantModels), again.GetActiveBackend().ModelsDir)
				backupAgain, err := os.ReadFile(path + EngineMigrationBackupSuffix)
				require.NoError(t, err)
				assert.Equal(t, body, string(backupAgain), "second start must not touch the backup")
			})
		}
	}
}

func TestNewConfigNotMigrated(t *testing.T) {
	for _, body := range []string{"active_sd: forge-neo\nactive_llm: ollama\n", "active_sd: sdcpp\nactive_llm: llamacpp\n"} {
		path := writeYAML(t, body)
		cfg, err := Load(path)
		require.NoError(t, err)
		assert.NotEmpty(t, cfg.ActiveLLM)
		_, err = os.Stat(path + EngineMigrationBackupSuffix)
		assert.True(t, os.IsNotExist(err), "no backup for %q", body)
		saved, _ := os.ReadFile(path)
		assert.Equal(t, body, string(saved))
	}
}

func TestRestoredBackupIsNotMigratedAgain(t *testing.T) {
	path := writeYAML(t, "active_sd: forge-neo\n")
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)

	old, err := os.ReadFile(path + EngineMigrationBackupSuffix)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, old, 0o644))

	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, NeoBackendKey, cfg.ActiveSD)
	assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)
	saved, _ := os.ReadFile(path)
	assert.Equal(t, "active_sd: forge-neo\n", string(saved))
}

func TestCustomBackendModelsKeptOnLaterStarts(t *testing.T) {
	for lname, load := range loaders("") {
		t.Run(lname, func(t *testing.T) {
			dataDir := t.TempDir()
			mine := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(mine, "m"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sd-webui-forge-neo", "models", "Stable-diffusion"), 0o755))
			body := "data_dir: " + filepath.ToSlash(dataDir) + "\nactive_sd: my-forge\n" + legacyProcesses +
				"backends:\n  my-forge:\n    name: Mine\n    process_key: sd\n    models_dir: " + filepath.ToSlash(mine) + "/m\n"
			path := writeYAML(t, body)
			for i := 0; i < 3; i++ {
				cfg, err := load(path)
				require.NoError(t, err)
				assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
				assert.Equal(t, filepath.Join(mine, "m"), cfg.GetActiveBackend().ModelsDir, "start %d", i+1)
			}
		})
	}
}

func TestMigrationKeepsFileMode(t *testing.T) {
	path := writeYAML(t, "active_sd: forge-neo\n")
	require.NoError(t, os.Chmod(path, 0o600))
	_, err := Load(path)
	require.NoError(t, err)
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err))
}

func TestMigrationInMemoryWhenConfigReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "server-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("active_sd: forge-neo\n"), 0o644))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
	assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)
	saved, _ := os.ReadFile(path)
	assert.Equal(t, "active_sd: forge-neo\n", string(saved))
}

func TestFailedSaveRetriesNextStart(t *testing.T) {
	path := writeYAML(t, "active_sd: forge-neo\n")
	require.NoError(t, os.MkdirAll(filepath.Join(path+".tmp", "x"), 0o755))
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)
	_, err = os.Stat(path + EngineMigrationBackupSuffix)
	assert.True(t, os.IsNotExist(err), "backup of a failed save must not look like a rollback")

	require.NoError(t, os.RemoveAll(path+".tmp"))
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
	assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)
	saved, _ := os.ReadFile(path)
	assert.Contains(t, string(saved), "active_llm: llamacpp")
}

func TestNoMigrationWithoutSDCppBuild(t *testing.T) {
	orig := sdcppOnThisPlatform
	sdcppOnThisPlatform = func() bool { return false }
	t.Cleanup(func() { sdcppOnThisPlatform = orig })

	for lname, load := range loaders("") {
		t.Run(lname, func(t *testing.T) {
			body := "data_dir: " + filepath.ToSlash(t.TempDir()) + "\nactive_sd: forge-neo\n" + legacyProcesses
			path := filepath.Join(t.TempDir(), "server-config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

			cfg, err := load(path)
			require.NoError(t, err)
			assert.Equal(t, "forge-neo", cfg.ActiveSD)
			assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)
			_, hasOllama := cfg.Processes["ollama"]
			assert.False(t, hasOllama)
			saved, _ := os.ReadFile(path)
			assert.Equal(t, body, string(saved))
			_, err = os.Stat(path + EngineMigrationBackupSuffix)
			assert.True(t, os.IsNotExist(err))
		})
	}
}
