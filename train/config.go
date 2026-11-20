package train

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const saveEvery = 250

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
{{pretrained}}      save:
        dtype: float16
        save_every: {{save_every}}
        max_step_saves_to_keep: 1
        push_to_hub: false
      datasets:
        - folder_path: {{dataset}}
          caption_ext: txt
{{dataset_extra}}          cache_latents_to_disk: true
          resolution: [{{resolution}}]
{{dataset_new}}      train:
        batch_size: 1
        steps: {{steps}}
        gradient_accumulation_steps: 1
        train_unet: true
        train_text_encoder: false
{{train_extra}}        gradient_checkpointing: true
        noise_scheduler: {{noise}}
        optimizer: adamw8bit
        lr: 1e-4
        dtype: bf16
        disable_sampling: true
      model:
{{model}}      sample:
        sampler: {{sampler}}
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
	resolution                      int
	ddpm                            bool
}

const flowmatchQuant = `        quantize: true
        quantize_te: true
        qtype: qfloat8
        low_vram: true
`

func zimageBlock(checkpoint, adapter string) modelBlock {
	return modelBlock{
		model: `        arch: zimage
        name_or_path: ` + yamlQuote(checkpoint) + `
        assistant_lora_path: ` + yamlQuote(adapter) + `
` + flowmatchQuant,
		datasetExtra: "          caption_dropout_rate: 0.05\n",
	}
}

func mossBlock(checkpoint, textEncoder, vae string) modelBlock {
	return modelBlock{
		model: `        arch: moss_image
        name_or_path: ` + yamlQuote(checkpoint) + `
        te_name_or_path: ` + yamlQuote(textEncoder) + `
        vae_path: ` + yamlQuote(vae) + `
` + flowmatchQuant,
		datasetExtra: "          cache_text_embeddings: true\n",
		trainExtra:   "        unload_text_encoder: true\n",
	}
}

func kleinBlock(checkpoint, vae string) modelBlock {
	return modelBlock{
		model: `        arch: flux2_klein_4b
        name_or_path: ` + yamlQuote(checkpoint) + `
        vae_path: ` + yamlQuote(vae) + `
` + flowmatchQuant,
	}
}

func flux1Block(configDir string) modelBlock {
	return modelBlock{
		model: `        name_or_path: ` + yamlQuote(configDir) + `
        is_flux: true
        quantize: true
        quantize_te: false
        qtype: qfloat8
        low_vram: true
`,
		datasetExtra: "          cache_text_embeddings: true\n",
		trainExtra:   "        unload_text_encoder: true\n",
		resolution:   512,
	}
}

func sdxlBlock(checkpoint string) modelBlock {
	return modelBlock{
		model: `        name_or_path: ` + yamlQuote(checkpoint) + `
        is_xl: true
        quantize: false
        low_vram: true
`,
		resolution: 1024,
		ddpm:       true,
	}
}

func writeBaseConfig(spec RunSpec, name, trigger string, b modelBlock) error {
	resolution, noise, sampler := 768, "flowmatch\n        timestep_type: weighted", "flowmatch"
	if b.resolution > 0 {
		resolution = b.resolution
	}
	if b.ddpm {
		noise, sampler = "ddpm", "ddpm"
	}
	pretrained, datasetNew := "", ""
	if spec.Pretrained != "" {
		pretrained = "        pretrained_lora_path: " + yamlQuote(spec.Pretrained) + "\n"
	}
	if spec.NewDatasetDir != "" {
		datasetNew = "        - folder_path: " + yamlQuote(spec.NewDatasetDir) + "\n" +
			"          caption_ext: txt\n" + b.datasetExtra +
			"          cache_latents_to_disk: true\n" +
			"          resolution: [" + strconv.Itoa(resolution) + "]\n" +
			"          num_repeats: " + strconv.Itoa(newRepeats) + "\n"
	}
	r := strings.NewReplacer(
		"{{pretrained}}", pretrained,
		"{{dataset_new}}", datasetNew,
		"{{resolution}}", strconv.Itoa(resolution),
		"{{noise}}", noise,
		"{{sampler}}", sampler,
		"{{model}}", b.model,
		"{{dataset_extra}}", b.datasetExtra,
		"{{train_extra}}", b.trainExtra,
		"{{name}}", yamlQuote(name),
		"{{trigger}}", yamlQuote(trigger),
		"{{steps}}", strconv.Itoa(spec.Steps),
		"{{save_every}}", strconv.Itoa(min(saveEvery, spec.Steps)),
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
