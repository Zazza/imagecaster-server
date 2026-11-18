package faceswap

import (
	"context"
	"fmt"
	"math"

	"imagecaster-server/onnxrt"
)

const (
	arcSize = 112
	embDim  = 512
)

var arcfaceDst = [5][2]float64{
	{38.2946, 51.6963}, {73.5318, 51.5014}, {56.0252, 71.7366}, {41.5493, 92.3655}, {70.7299, 92.2041},
}

func estimateNorm(kps [5][2]float64, size int) affine {
	ratio, diff := float64(size)/112, 0.0
	if size%112 != 0 {
		ratio = float64(size) / 128
		diff = 8 * ratio
	}
	var dst [5][2]float64
	for i, p := range arcfaceDst {
		dst[i] = [2]float64{p[0]*ratio + diff, p[1] * ratio}
	}
	return similarity(kps, dst)
}

func normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	n := math.Sqrt(s)
	out := make([]float32, len(v))
	if n == 0 {
		return out
	}
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

func Similarity(a, b []float32) float64 {
	a, b = normalize(a), normalize(b)
	var s float64
	for i := range a {
		if i < len(b) {
			s += float64(a[i]) * float64(b[i])
		}
	}
	return s
}

func (e *Engine) embed(ctx context.Context, src fetchFn, kps [5][2]float64) ([]float32, error) {
	s, err := e.session(modelArc)
	if err != nil {
		return nil, err
	}
	crop := warp(src, 3, estimateNorm(kps, arcSize), arcSize, arcSize, true)
	in := chw(crop, func(v float32, _ int) float32 { return (v - 127.5) / 127.5 })
	outs, _, err := onnxrt.Run(ctx, s, onnxrt.Input{Name: s.Inputs[0].Name, Shape: []int64{1, 3, arcSize, arcSize}, Data: in})
	if err != nil {
		return nil, fmt.Errorf("arcface: %w", err)
	}
	if len(outs[0]) != embDim {
		return nil, fmt.Errorf("arcface: %d values, want %d", len(outs[0]), embDim)
	}
	return normalize(outs[0]), nil
}
