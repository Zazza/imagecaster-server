package train

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const trainConfigTemplate = `job: extension
config:
  name: {{name}}
  process:
    - type: sd_trainer
      training_folder: {{output}}
      device: cuda:0
      trigger_word: {{trigger}}
      network:
        type: lora
        linear: 16
        linear_alpha: 16
      save:
        dtype: float16
        save_every: {{steps}}
        max_step_saves_to_keep: 1
        push_to_hub: false
      datasets:
        - folder_path: {{dataset}}
          caption_ext: txt
{{dataset_extra}}          cache_latents_to_disk: true
          resolution: [768]
      train:
        batch_size: 1
        steps: {{steps}}
        gradient_accumulation_steps: 1
        train_unet: true
        train_text_encoder: false
{{train_extra}}        gradient_checkpointing: true
        noise_scheduler: flowmatch
        timestep_type: weighted
        optimizer: adamw8bit
        lr: 1e-4
        dtype: bf16
        disable_sampling: true
      model:
{{model}}      sample:
        sampler: flowmatch
        sample_every: 1000000
        width: 1024
        height: 1024
        prompts: [{{trigger}}]
        guidance_scale: 1
        sample_steps: 9
meta:
  name: {{name}}
  version: '1.0'
`

func yamlQuote(s string) string {
	return strconv.Quote(filepath.ToSlash(s))
}

type modelBlock struct {
	model, datasetExtra, trainExtra string
}

var zimageBlock = modelBlock{
	model: `        arch: zimage
        name_or_path: Tongyi-MAI/Z-Image-Turbo
        assistant_lora_path: ostris/zimage_turbo_training_adapter/zimage_turbo_training_adapter_v2.safetensors
        quantize: true
        quantize_te: true
        qtype: qfloat8
        low_vram: true
`,
	datasetExtra: "          caption_dropout_rate: 0.05\n",
}

func mossBlock(f mossSet) modelBlock {
	return modelBlock{
		model: `        arch: moss_image
        name_or_path: ` + yamlQuote(f.checkpoint) + `
        te_name_or_path: ` + yamlQuote(f.textEncoder) + `
        vae_path: ` + yamlQuote(f.vae) + `
        quantize: true
        quantize_te: true
        qtype: qfloat8
        low_vram: true
`,
		datasetExtra: "          cache_text_embeddings: true\n",
		trainExtra:   "        unload_text_encoder: true\n",
	}
}

func writeTrainConfig(spec RunSpec, name, trigger string) error {
	return writeBaseConfig(spec, name, trigger, zimageBlock)
}

func writeBaseConfig(spec RunSpec, name, trigger string, b modelBlock) error {
	r := strings.NewReplacer(
		"{{model}}", b.model,
		"{{dataset_extra}}", b.datasetExtra,
		"{{train_extra}}", b.trainExtra,
		"{{name}}", yamlQuote(name),
		"{{trigger}}", yamlQuote(trigger),
		"{{steps}}", strconv.Itoa(spec.Steps),
		"{{output}}", yamlQuote(filepath.Dir(filepath.Dir(spec.OutputPath))),
		"{{dataset}}", yamlQuote(spec.DatasetDir),
	)
	return os.WriteFile(spec.ConfigPath, []byte(r.Replace(trainConfigTemplate)), 0o644)
}

func writeCaptions(dir, gender string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	caption := fmt.Sprintf("photo of [trigger], a %s", gender)
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || ext == ".txt" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, strings.TrimSuffix(e.Name(), ext)+".txt"), []byte(caption), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
