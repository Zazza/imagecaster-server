package sdcpp

import (
	"context"
	"encoding/json"
	"imagecaster-server/imgb64"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (f *Facade) resolveUpscaler(name string, hires bool) (string, error) {
	n := strings.TrimSpace(name)
	switch {
	case n == "" && hires, n == "latent" && hires:
		return "Latent", nil
	case n == "", n == "lanczos":
		return "Lanczos", nil
	case n == "nearest":
		return "Nearest", nil
	}
	for _, u := range f.upscalerFiles() {
		base := filepath.Base(u)
		if strings.TrimSuffix(base, filepath.Ext(base)) == n {
			return n, nil
		}
	}
	return "", apiErr(http.StatusBadRequest, "invalid_params", "upscaler %q is not available on this server", name)
}

func (f *Facade) upscalerFiles() []string {
	if f.cfg.UpscalerDir == "" {
		return nil
	}
	ents, err := os.ReadDir(f.cfg.UpscalerDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".pth", ".safetensors", ".bin", ".gguf":
			out = append(out, filepath.Join(f.cfg.UpscalerDir, e.Name()))
		}
	}
	return out
}

func (p GenParams) withDefaults() GenParams {
	if p.Steps <= 0 {
		p.Steps = 20
	}
	if p.CfgScale == nil {
		p.CfgScale = ptr(7.0)
	}
	if p.DenoisingStrength == nil {
		p.DenoisingStrength = ptr(0.75)
	}
	if p.BatchSize <= 0 {
		p.BatchSize = 1
	}
	return p
}

func ptr[T any](v T) *T {
	return &v
}

func (f *Facade) buildNative(ctx context.Context, p GenParams, img2img bool, m ModelSpec) (map[string]any, error) {
	prompt, loras, err := f.extractLoras(ctx, p.Prompt)
	if err != nil {
		return nil, err
	}
	guidance := map[string]any{"txt_cfg": *p.CfgScale}
	if p.DistilledCfgScale != nil {
		guidance["distilled_guidance"] = *p.DistilledCfgScale
	}
	sp := map[string]any{"sample_steps": p.Steps, "guidance": guidance}
	if p.Sampler != "" {
		sp["sample_method"] = p.Sampler
	}
	if p.Scheduler != "" {
		sp["scheduler"] = p.Scheduler
	}
	n := map[string]any{
		"prompt":          prompt,
		"negative_prompt": loraTag.ReplaceAllString(p.NegativePrompt, ""),
		"sample_params":   sp,
		"lora":            loras,
		"output_format":   "png",
		"preview":         "none",
	}
	if p.Width > 0 {
		n["width"] = p.Width
	}
	if p.Height > 0 {
		n["height"] = p.Height
	}
	if p.ClipSkip != nil && *p.ClipSkip > 1 {
		n["clip_skip"] = *p.ClipSkip
	}
	if p.CacheMode != "" {
		n["cache_mode"] = p.CacheMode
		n["cache_option"] = p.CacheOption
	}
	if img2img {
		n["init_image"] = p.InitImage
		n["strength"] = *p.DenoisingStrength
		if m.KleinReference {
			n["ref_images"] = []string{p.InitImage}
		}
		if p.Mask != "" {
			n["mask_image"] = p.Mask
		}
	} else if p.Hires != nil && p.Hires.Enable {
		up, err := f.resolveUpscaler(p.Hires.Upscaler, true)
		if err != nil {
			return nil, err
		}
		h := map[string]any{"enabled": true, "upscaler": up, "target_width": p.Hires.ResizeX, "target_height": p.Hires.ResizeY, "steps": p.Hires.Steps}
		if p.Hires.Scale != nil {
			h["scale"] = *p.Hires.Scale
		}
		if p.Hires.Denoising != nil {
			h["denoising_strength"] = *p.Hires.Denoising
		}
		n["hires"] = h
	}
	return n, nil
}

func (f *Facade) generate(ctx context.Context, j *job, m ModelSpec, p GenParams) ([]string, map[string]any, error) {
	if err := f.ensureModel(ctx, m); err != nil {
		return nil, nil, err
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	img2img := j.kind == kindImg2Img
	native, err := f.buildNative(ctx, p, img2img, m)
	if err != nil {
		return nil, nil, err
	}
	native["batch_count"] = p.BatchSize
	seed := int64(-1)
	if p.Seed != nil {
		seed = *p.Seed
	}
	if seed < 0 {
		seed = rand.Int64N(1 << 32)
	}
	native["seed"] = seed
	imgs, err := f.runNative(ctx, native, j)
	if err != nil {
		return nil, nil, err
	}
	seeds := make([]int64, len(imgs))
	for i := range imgs {
		seeds[i] = seed + int64(i)
	}
	width, height := p.Width, p.Height
	if (width <= 0 || height <= 0) && len(imgs) > 0 {
		if w, h, err := imgb64.Size(imgs[0]); err == nil {
			width, height = w, h
		} else {
			log.Printf("[sdcpp] job %s: read result size: %v", j.st.ID, err)
		}
	}
	info := map[string]any{
		"prompt":          p.Prompt,
		"negative_prompt": p.NegativePrompt,
		"seed":            seed,
		"all_seeds":       seeds,
		"width":           width,
		"height":          height,
		"sampler":         p.Sampler,
		"scheduler":       p.Scheduler,
		"steps":           p.Steps,
		"cfg_scale":       *p.CfgScale,
		"batch_size":      p.BatchSize,
		"checkpoint":      m.Checkpoint,
		"vae":             m.VAE,
		"modules":         m.Modules,
		"sd_model_name":   stem(cleanName(m.Checkpoint)),
		"backend":         "sd.cpp",
	}
	if img2img {
		info["denoising_strength"] = *p.DenoisingStrength
	}
	if p.CacheMode != "" {
		info["cache_mode"] = p.CacheMode
		if p.CacheOption != "" {
			info["cache_option"] = p.CacheOption
		}
	}
	return imgs, info, nil
}

type nativeJob struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Progress struct {
		Completed *int   `json:"completed"`
		Total     *int   `json:"total"`
		Unit      string `json:"unit"`
	} `json:"progress"`
	Result *struct {
		Images []struct {
			B64 string `json:"b64_json"`
		} `json:"images"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (f *Facade) runNative(ctx context.Context, native map[string]any, j *job) ([]string, error) {
	code, body, err := f.do(ctx, http.MethodPost, "/sdcpp/v1/img_gen", native, 60*time.Second)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable: %v", err)
	}
	if code != http.StatusAccepted && code != http.StatusOK {
		return nil, submitError(code, body)
	}
	var sub nativeJob
	if err := json.Unmarshal(body, &sub); err != nil || sub.ID == "" {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server img_gen: bad response: %s", truncate(body, 200))
	}
	fails := 0
	for {
		select {
		case <-ctx.Done():
			f.cancelNative(sub.ID)
			return nil, ctx.Err()
		case <-time.After(f.cfg.PollInterval):
		}
		code, body, err := f.do(ctx, http.MethodGet, "/sdcpp/v1/jobs/"+sub.ID, nil, 30*time.Second)
		if ctx.Err() != nil {
			f.cancelNative(sub.ID)
			return nil, ctx.Err()
		}
		if err != nil || code >= 500 {
			fails++
			if fails >= 5 {
				if err != nil {
					return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable while generating: %v", err)
				}
				return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server job HTTP %d: %s", code, truncate(body, 200))
			}
			continue
		}
		fails = 0
		if code != http.StatusOK {
			return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server job %s HTTP %d: %s", sub.ID, code, truncate(body, 200))
		}
		var nj nativeJob
		if err := json.Unmarshal(body, &nj); err != nil {
			return nil, apiErr(http.StatusBadGateway, "sd_failed", "decode job: %v", err)
		}
		step, steps := 0, 0
		if nj.Progress.Unit == "steps" && nj.Progress.Total != nil && *nj.Progress.Total > 0 && nj.Progress.Completed != nil {
			step, steps = *nj.Progress.Completed, *nj.Progress.Total
		}
		f.jobs.markRunning(j, step, steps)
		switch nj.Status {
		case "completed":
			imgs := []string{}
			if nj.Result != nil {
				for _, im := range nj.Result.Images {
					imgs = append(imgs, im.B64)
				}
			}
			return imgs, nil
		case "failed":
			msg := "generation failed"
			if nj.Error != nil && nj.Error.Message != "" {
				msg = nj.Error.Message
			}
			return nil, apiErr(http.StatusInternalServerError, "sd_failed", "sd.cpp: %s", msg)
		case "cancelled":
			return nil, apiErr(http.StatusInternalServerError, "cancelled", "sd.cpp job cancelled")
		}
	}
}

func submitError(code int, body []byte) error {
	var e struct {
		Error any `json:"error"`
	}
	msg := truncate(body, 400)
	if json.Unmarshal(body, &e) == nil && e.Error != nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				msg = m
			}
		}
	}
	return apiErr(http.StatusBadGateway, "sd_failed", "sd.cpp HTTP %d: %s", code, msg)
}

func (f *Facade) cancelNative(id string) {
	if id == "" {
		return
	}
	if _, _, err := f.do(context.Background(), http.MethodPost, "/sdcpp/v1/jobs/"+id+"/cancel", nil, 10*time.Second); err != nil {
		log.Printf("[sdcpp] cancel %s: %v", id, err)
	}
}
