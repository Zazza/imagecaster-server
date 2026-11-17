package main

import (
	"context"
	"log"
	"path/filepath"

	"imagecaster-server/config"
	"imagecaster-server/models"
	"imagecaster-server/sdcpp"
	"imagecaster-server/train"
)

func trainLoraDir(cfg *config.Config) string {
	b := cfg.GetActiveBackend()
	if b == nil || b.Kind != config.KindSDCpp {
		return ""
	}
	pc, ok := cfg.Process(b.ProcessKey)
	if !ok {
		return b.LoraDir
	}
	return effectiveDir(pc.Args, pc.WorkDir, "--lora-model-dir", b.LoraDir)
}

func newTrainManager(cfg *config.Config, gate *Gate, sd *sdcpp.Facade) *train.Manager {
	loraDir := trainLoraDir(cfg)
	if cfg.DataDir == "" || loraDir == "" || sd == nil {
		return nil
	}
	if !gate.Enabled() {
		log.Printf("[train] disabled: the GPU queue is off")
		return nil
	}
	var m *train.Manager
	leaser := func(ctx context.Context, client string) (func(), error) {
		release, err := gate.LeaseTrain(ctx, client, func() {
			log.Printf("[train] GPU lease lost, stopping training")
			m.CancelRunning()
		})
		if err != nil {
			return nil, err
		}
		if err := sd.UnloadModel(ctx); err != nil {
			log.Printf("[train] unload sd model: %v", err)
		}
		return release, nil
	}
	b := cfg.GetActiveBackend()
	m, err := train.New(train.Config{
		Python:     cfg.Train.Python,
		ToolkitDir: cfg.Train.ToolkitDir,
		JobsDir:    filepath.Join(cfg.DataDir, "trainjobs"),
		LoraDir:    loraDir,
		Steps:      cfg.Train.Steps,

		ModelsDir:      b.ModelsDir,
		TextEncoderDir: b.TextEncoderDir,
		VAEDir:         b.VaeDir,
		TrainDir:       models.TrainDir(b),
	}, train.NewProcessRunner(cfg.Train.Python, cfg.Train.ToolkitDir), leaser)
	if err != nil {
		log.Printf("[train] disabled: %v", err)
		return nil
	}
	return m
}
