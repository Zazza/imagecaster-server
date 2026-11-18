package faceswap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	ort "github.com/getcharzp/onnxruntime_purego"

	"imagecaster-server/onnxrt"
)

const (
	modelDet     = "det_10g.onnx"
	modelLmk     = "1k3d68.onnx"
	modelArc     = "w600k_r50.onnx"
	modelSwap    = "inswapper_128.onnx"
	modelParse   = "bisenet_resnet_34.onnx"
	modelRestore = "gfpgan_1.4.onnx"

	defaultIdle = 5 * time.Minute
)

var Models = []string{modelDet, modelLmk, modelArc, modelSwap, modelParse, modelRestore}

var ErrUnavailable = errors.New("face models are not installed")

type Config struct {
	LibPath   string
	ModelsDir string
	Threads   int
	Idle      time.Duration
}

type Engine struct {
	cfg Config

	mu    sync.Mutex
	sess  map[string]*ort.Session
	emap  []float32
	timer *time.Timer
}

func New(cfg Config) *Engine {
	if cfg.Idle <= 0 {
		cfg.Idle = defaultIdle
	}
	return &Engine{cfg: cfg, sess: map[string]*ort.Session{}}
}

func (e *Engine) modelsPresent() bool {
	if e == nil || e.cfg.ModelsDir == "" {
		return false
	}
	for _, m := range Models {
		st, err := os.Stat(filepath.Join(e.cfg.ModelsDir, m))
		if err != nil || !st.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func (e *Engine) Available() bool {
	if !e.modelsPresent() {
		return false
	}
	_, err := onnxrt.Shared(e.cfg.LibPath)
	return err == nil
}

func (e *Engine) session(name string) (*ort.Session, error) {
	if s, ok := e.sess[name]; ok {
		return s, nil
	}
	if !e.modelsPresent() {
		return nil, ErrUnavailable
	}
	eng, err := onnxrt.Shared(e.cfg.LibPath)
	if err != nil {
		return nil, fmt.Errorf("%w: load ONNX Runtime: %v", ErrUnavailable, err)
	}
	path := filepath.Join(e.cfg.ModelsDir, name)
	var s *ort.Session
	if name == modelSwap {
		s, err = e.openSwapper(eng, path)
	} else {
		s, err = onnxrt.Open(eng, path, e.cfg.Threads)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load %s: %v", ErrUnavailable, name, err)
	}
	e.sess[name] = s
	return s, nil
}

func (e *Engine) openSwapper(eng *ort.Engine, path string) (*ort.Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	emap, err := lastInitializer(data)
	if err != nil {
		return nil, err
	}
	if len(emap) != embDim*embDim {
		return nil, fmt.Errorf("emap has %d values, want %d", len(emap), embDim*embDim)
	}
	opts, err := onnxrt.Options(eng, e.cfg.Threads)
	if err != nil {
		return nil, err
	}
	defer opts.Close()
	s, err := eng.NewSessionFromBytes(data, opts)
	if err != nil {
		return nil, err
	}
	e.emap = emap
	return s, nil
}

func (e *Engine) lock() func() {
	e.mu.Lock()
	return func() {
		if e.timer == nil {
			e.timer = time.AfterFunc(e.cfg.Idle, e.Unload)
		} else {
			e.timer.Reset(e.cfg.Idle)
		}
		e.mu.Unlock()
	}
}

func (e *Engine) Unload() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for name, s := range e.sess {
		s.Close()
		delete(e.sess, name)
	}
	e.emap = nil
}
