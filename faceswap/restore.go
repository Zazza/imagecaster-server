package faceswap

import (
	"context"
	"fmt"

	"imagecaster-server/onnxrt"
)

const restoreSize = 512

var (
	ffhq512 = [5][2]float64{
		{0.37691676 * 512, 0.46864664 * 512}, {0.62285697 * 512, 0.46912813 * 512}, {0.50123859 * 512, 0.61331904 * 512},
		{0.39308822 * 512, 0.72541100 * 512}, {0.61150205 * 512, 0.72490465 * 512},
	}
	faceClasses   = map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 10: true, 11: true, 12: true, 13: true}
	imagenetMean  = [3]float32{0.485, 0.456, 0.406}
	imagenetStd   = [3]float32{0.229, 0.224, 0.225}
	restoreShape  = []int64{1, 3, restoreSize, restoreSize}
	restorePixels = restoreSize * restoreSize
)

func (e *Engine) restore(ctx context.Context, crop plane) (plane, error) {
	s, err := e.session(modelRestore)
	if err != nil {
		return plane{}, err
	}
	in := chw(crop, func(v float32, _ int) float32 { return (v/255 - 0.5) / 0.5 })
	outs, _, err := onnxrt.Run(ctx, s, onnxrt.Input{Name: s.Inputs[0].Name, Shape: restoreShape, Data: in})
	if err != nil {
		return plane{}, fmt.Errorf("gfpgan: %w", err)
	}
	if len(outs[0]) != 3*restorePixels {
		return plane{}, fmt.Errorf("gfpgan: %d values", len(outs[0]))
	}
	return fromCHW(outs[0], restoreSize, restoreSize, func(v float32) float32 {
		return trunc8((min(max(v, -1), 1) + 1) / 2 * 255)
	}), nil
}

func (e *Engine) parseMask(ctx context.Context, crop plane) (plane, error) {
	s, err := e.session(modelParse)
	if err != nil {
		return plane{}, err
	}
	in := chw(crop, func(v float32, ch int) float32 { return (v/255 - imagenetMean[ch]) / imagenetStd[ch] })
	outs, _, err := onnxrt.Run(ctx, s, onnxrt.Input{Name: s.Inputs[0].Name, Shape: restoreShape, Data: in})
	if err != nil {
		return plane{}, fmt.Errorf("bisenet: %w", err)
	}
	seg := outs[0]
	classes := len(seg) / restorePixels
	if classes < 14 || len(seg) != classes*restorePixels {
		return plane{}, fmt.Errorf("bisenet: %d values", len(seg))
	}
	m := newPlane(restoreSize, restoreSize, 1)
	for i := 0; i < restorePixels; i++ {
		best, bc := seg[i], 0
		for c := 1; c < classes; c++ {
			if v := seg[c*restorePixels+i]; v > best {
				best, bc = v, c
			}
		}
		if faceClasses[bc] {
			m.pix[i] = 1
		}
	}
	return blur(erode(m, 9), 31), nil
}
