package faceswap

import (
	"context"
	"image"
	"math"
)

type FaceInfo struct {
	Box        [4]float64 `json:"box"`
	HeightPx   int        `json:"height_px"`
	Score      float64    `json:"score"`
	Yaw        float64    `json:"yaw"`
	Sharpness  float64    `json:"sharpness"`
	Embedding  []float32  `json:"embedding"`
	Similarity *float64   `json:"similarity,omitempty"`
}

const maxAnalyzeFaces = 8

func (e *Engine) AnalyzeFaces(ctx context.Context, img image.Image, refs [][]float32) ([]FaceInfo, error) {
	unlock := e.lock()
	defer unlock()
	p := fromImage(img)
	pad := max(p.w, p.h) / 2
	src := p.padded(pad)
	faces, err := e.detect(ctx, src, p.w+2*pad, p.h+2*pad)
	if err != nil {
		return nil, err
	}
	if len(faces) > maxAnalyzeFaces {
		faces = faces[:maxAnalyzeFaces]
	}
	out := make([]FaceInfo, 0, len(faces))
	for _, f := range faces {
		yaw, err := e.yaw(ctx, src, f.box)
		if err != nil {
			return nil, err
		}
		emb, err := e.embed(ctx, src, f.kps)
		if err != nil {
			return nil, err
		}
		emb = normalize(emb)
		box := [4]float64{f.box[0] - float64(pad), f.box[1] - float64(pad), f.box[2] - float64(pad), f.box[3] - float64(pad)}
		info := FaceInfo{
			Box:       [4]float64{clampUnit(box[0] / float64(p.w)), clampUnit(box[1] / float64(p.h)), clampUnit(box[2] / float64(p.w)), clampUnit(box[3] / float64(p.h))},
			HeightPx:  int(math.Round(f.height())),
			Score:     f.score,
			Yaw:       yaw,
			Sharpness: faceSharpness(p, box),
			Embedding: emb,
		}
		if len(refs) > 0 {
			best := -1.0
			for _, r := range refs {
				if len(r) == len(emb) {
					best = math.Max(best, Similarity(emb, r))
				}
			}
			if best > -1 {
				info.Similarity = &best
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func clampUnit(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func faceSharpness(p plane, box [4]float64) float64 {
	x0, y0 := clampInt(int(box[0]), 0, p.w-1), clampInt(int(box[1]), 0, p.h-1)
	x1, y1 := clampInt(int(box[2]), 0, p.w-1), clampInt(int(box[3]), 0, p.h-1)
	w, h := x1-x0, y1-y0
	if w < 8 || h < 8 {
		return 0
	}
	const side = 128
	gray := func(x, y int) float64 {
		sx := x0 + x*w/side
		sy := y0 + y*h/side
		return 0.299*float64(p.at(sx, sy, 0)) + 0.587*float64(p.at(sx, sy, 1)) + 0.114*float64(p.at(sx, sy, 2))
	}
	var sum, sq float64
	n := 0
	for y := 1; y < side-1; y++ {
		for x := 1; x < side-1; x++ {
			l := 4*gray(x, y) - gray(x-1, y) - gray(x+1, y) - gray(x, y-1) - gray(x, y+1)
			sum += l
			sq += l * l
			n++
		}
	}
	mean := sum / float64(n)
	return sq/float64(n) - mean*mean
}
