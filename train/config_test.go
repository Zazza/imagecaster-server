package train

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestTrainConfigKeepsNamesAsStrings(t *testing.T) {
	for _, name := range []string{"12", "null", "true", "false", "1_0", "anna"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			spec := RunSpec{
				ConfigPath: filepath.Join(dir, "config.yaml"),
				DatasetDir: filepath.Join(dir, "dataset"),
				OutputPath: filepath.Join(dir, "output", name, name+".safetensors"),
				Steps:      100,
			}
			require.NoError(t, writeTrainConfig(spec, name, name+"_man"))
			data, err := os.ReadFile(spec.ConfigPath)
			require.NoError(t, err)
			var doc struct {
				Config struct {
					Name    any `yaml:"name"`
					Process []struct {
						TrainingFolder string `yaml:"training_folder"`
						TriggerWord    any    `yaml:"trigger_word"`
					} `yaml:"process"`
				} `yaml:"config"`
			}
			require.NoError(t, yaml.Unmarshal(data, &doc))
			assert.Equal(t, name, doc.Config.Name)
			require.Len(t, doc.Config.Process, 1)
			assert.Equal(t, name+"_man", doc.Config.Process[0].TriggerWord)
			assert.Equal(t, filepath.ToSlash(filepath.Join(dir, "output")), doc.Config.Process[0].TrainingFolder)
		})
	}
}
