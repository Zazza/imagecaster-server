package onnxrt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/getcharzp/onnxruntime_purego"
)

var (
	mu     sync.Mutex
	shared *ort.Engine
)

func LibCandidates(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	var names []string
	switch runtime.GOOS {
	case "windows":
		names = []string{"onnxruntime.dll"}
	case "darwin":
		names = []string{"libonnxruntime.dylib"}
	default:
		names = []string{"libonnxruntime.so", "libonnxruntime.so.1"}
	}
	var out []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, n := range names {
			out = append(out, filepath.Join(dir, n), filepath.Join(dir, "onnxruntime", n))
		}
	}
	if runtime.GOOS == "linux" {
		out = append(out, names...)
	}
	return out
}

func Shared(libPath string) (*ort.Engine, error) {
	mu.Lock()
	defer mu.Unlock()
	if shared != nil {
		return shared, nil
	}
	lastErr := errors.New("no ONNX Runtime library candidates")
	for _, p := range LibCandidates(libPath) {
		if filepath.IsAbs(p) {
			if _, err := os.Stat(p); err != nil {
				lastErr = err
				continue
			}
		}
		e, err := ort.NewEngine(p)
		if err == nil {
			shared = e
			return e, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func Options(eng *ort.Engine, threads int) (*ort.SessionOptions, error) {
	opts, err := eng.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	if threads > 0 {
		if err := opts.SetIntraOpNumThreads(threads); err != nil {
			opts.Close()
			return nil, err
		}
	}
	return opts, nil
}

func Open(eng *ort.Engine, path string, threads int) (*ort.Session, error) {
	opts, err := Options(eng, threads)
	if err != nil {
		return nil, err
	}
	defer opts.Close()
	return eng.NewSession(path, opts)
}

type Input struct {
	Name  string
	Shape []int64
	Data  []float32
}

func Run(ctx context.Context, s *ort.Session, inputs ...Input) ([][]float32, [][]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	feed := make(map[string]*ort.Value, len(inputs))
	defer func() {
		for _, v := range feed {
			v.Close()
		}
	}()
	for _, in := range inputs {
		v, err := ort.NewTensor(in.Shape, in.Data)
		if err != nil {
			return nil, nil, err
		}
		feed[in.Name] = v
	}
	outs, err := s.Run(feed)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		for _, o := range outs {
			o.Close()
		}
	}()
	var datas [][]float32
	var shapes [][]int64
	for _, info := range s.Outputs {
		v, ok := outs[info.Name]
		if !ok {
			return nil, nil, fmt.Errorf("output %q missing", info.Name)
		}
		d, err := ort.GetTensorData[float32](v)
		if err != nil {
			return nil, nil, err
		}
		sh, err := v.GetShape()
		if err != nil {
			return nil, nil, err
		}
		datas = append(datas, append([]float32(nil), d...))
		shapes = append(shapes, append([]int64(nil), sh...))
	}
	return datas, shapes, nil
}
