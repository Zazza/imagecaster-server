package sdcpp

import (
	"context"
	"time"
)

const (
	kindTxt2Img = "txt2img"
	kindImg2Img = "img2img"
	kindUpscale = "upscale"

	statusQueued    = "queued"
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
	statusLost      = "lost"

	maxActiveJobs = 100
	maxJobBody    = 512 << 20
)

type ModelSpec struct {
	Checkpoint     string   `json:"checkpoint"`
	VAE            string   `json:"vae"`
	Modules        []string `json:"modules"`
	KleinReference bool     `json:"klein_reference"`
	MossReference  bool     `json:"moss_reference"`
}

type HiresParams struct {
	Enable    bool     `json:"enable"`
	Scale     *float64 `json:"scale"`
	Upscaler  string   `json:"upscaler"`
	Denoising *float64 `json:"denoising"`
	Steps     int      `json:"steps"`
	ResizeX   int      `json:"resize_x"`
	ResizeY   int      `json:"resize_y"`
}

type GenParams struct {
	Prompt            string       `json:"prompt"`
	NegativePrompt    string       `json:"negative_prompt"`
	Sampler           string       `json:"sampler"`
	Scheduler         string       `json:"scheduler"`
	Steps             int          `json:"steps"`
	CfgScale          *float64     `json:"cfg_scale"`
	DistilledCfgScale *float64     `json:"distilled_cfg_scale"`
	Width             int          `json:"width"`
	Height            int          `json:"height"`
	Seed              *int64       `json:"seed"`
	ClipSkip          *int         `json:"clip_skip"`
	BatchSize         int          `json:"batch_size"`
	Hires             *HiresParams `json:"hires"`
	InitImage         string       `json:"init_image"`
	Mask              string       `json:"mask"`
	DenoisingStrength *float64     `json:"denoising_strength"`
	Image             string       `json:"image"`
	Upscaler          string       `json:"upscaler"`
	Scale             *float64     `json:"scale"`
	CacheMode         string       `json:"cache_mode"`
	CacheOption       string       `json:"cache_option"`
}

type jobRequest struct {
	Kind      string     `json:"kind"`
	Client    string     `json:"client"`
	ClientRef string     `json:"client_ref"`
	Model     *ModelSpec `json:"model"`
	Params    *GenParams `json:"params"`
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type JobProgress struct {
	Step  int `json:"step"`
	Steps int `json:"steps"`
}

type JobStatus struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"`
	Client     string      `json:"client"`
	ClientRef  string      `json:"client_ref"`
	Status     string      `json:"status"`
	Position   int         `json:"position"`
	Progress   JobProgress `json:"progress"`
	Error      *JobError   `json:"error"`
	CreatedAt  *time.Time  `json:"created_at"`
	StartedAt  *time.Time  `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at"`
	ExpiresAt  *time.Time  `json:"expires_at"`
}

type jobResult struct {
	Images []string       `json:"images"`
	Info   map[string]any `json:"info"`
}

type job struct {
	st      JobStatus
	model   ModelSpec
	params  GenParams
	kind    string
	cancel  context.CancelFunc
	ctx     context.Context
	result  *jobResult
	deleted bool
	stashed []string
}
