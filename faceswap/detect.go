package faceswap

import (
	"context"
	"fmt"
	"math"
	"sort"

	"imagecaster-server/onnxrt"
)

const (
	detSize   = 640
	detThresh = 0.5
	nmsThresh = 0.4
)

type face struct {
	box   [4]float64
	score float64
	kps   [5][2]float64
	yaw   float64
	emb   []float32
}

func (f face) height() float64 { return float64(float32(f.box[3]) - float32(f.box[1])) }

func (f face) area() float64 { return (f.box[2] - f.box[0]) * (f.box[3] - f.box[1]) }

func decodeSCRFD(outs [][]float32, size int, thr, scale float64) ([]face, error) {
	strides := []int{8, 16, 32}
	if len(outs) != 3*len(strides) {
		return nil, fmt.Errorf("scrfd: %d outputs, want 9", len(outs))
	}
	var faces []face
	for idx, stride := range strides {
		w := size / stride
		n := w * (size / stride) * 2
		scores, boxes, kps := outs[idx], outs[idx+3], outs[idx+6]
		if len(scores) != n || len(boxes) != 4*n || len(kps) != 10*n {
			return nil, fmt.Errorf("scrfd: stride %d: unexpected output sizes", stride)
		}
		s := float64(stride)
		for i := 0; i < n; i++ {
			if float64(scores[i]) < thr {
				continue
			}
			cell := i / 2
			cx, cy := float64(cell%w)*s, float64(cell/w)*s
			d := boxes[4*i : 4*i+4]
			f := face{score: float64(scores[i]), box: [4]float64{
				(cx - float64(d[0])*s) / scale, (cy - float64(d[1])*s) / scale,
				(cx + float64(d[2])*s) / scale, (cy + float64(d[3])*s) / scale,
			}}
			for k := 0; k < 5; k++ {
				f.kps[k] = [2]float64{(cx + float64(kps[10*i+2*k])*s) / scale, (cy + float64(kps[10*i+2*k+1])*s) / scale}
			}
			faces = append(faces, f)
		}
	}
	sort.SliceStable(faces, func(a, b int) bool { return faces[a].score > faces[b].score })
	return nms(faces, nmsThresh), nil
}

func nms(faces []face, thr float64) []face {
	var keep []face
	area := func(b [4]float64) float64 { return (b[2] - b[0] + 1) * (b[3] - b[1] + 1) }
	order := faces
	for len(order) > 0 {
		i := order[0]
		keep = append(keep, i)
		var rest []face
		for _, j := range order[1:] {
			w := math.Max(0, math.Min(i.box[2], j.box[2])-math.Max(i.box[0], j.box[0])+1)
			h := math.Max(0, math.Min(i.box[3], j.box[3])-math.Max(i.box[1], j.box[1])+1)
			inter := w * h
			if inter/(area(i.box)+area(j.box)-inter) <= thr {
				rest = append(rest, j)
			}
		}
		order = rest
	}
	return keep
}

func (e *Engine) detect(ctx context.Context, src fetchFn, w, h int) ([]face, error) {
	s, err := e.session(modelDet)
	if err != nil {
		return nil, err
	}
	nw, nh := detSize, int(float64(detSize)*float64(h)/float64(w))
	if float64(h)/float64(w) > 1 {
		nh, nw = detSize, int(float64(detSize)/(float64(h)/float64(w)))
	}
	clamped := func(x, y, ch int) float32 { return src(clampInt(x, 0, w-1), clampInt(y, 0, h-1), ch) }
	resized := resizeLinear(clamped, w, h, 3, nw, nh)
	const n = detSize * detSize
	input := make([]float32, 3*n)
	for i := range input {
		input[i] = -127.5 / 128
	}
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			for ch := 0; ch < 3; ch++ {
				input[ch*n+y*detSize+x] = (resized.pix[(y*nw+x)*3+ch] - 127.5) / 128
			}
		}
	}
	outs, _, err := onnxrt.Run(ctx, s, onnxrt.Input{Name: s.Inputs[0].Name, Shape: []int64{1, 3, detSize, detSize}, Data: input})
	if err != nil {
		return nil, fmt.Errorf("scrfd: %w", err)
	}
	return decodeSCRFD(outs, detSize, detThresh, float64(nh)/float64(h))
}

func (e *Engine) largestFace(ctx context.Context, img plane, withYaw, withEmb bool) (face, bool, error) {
	pad := max(img.w, img.h) / 2
	src := img.padded(pad)
	pw, ph := img.w+2*pad, img.h+2*pad
	faces, err := e.detect(ctx, src, pw, ph)
	if err != nil || len(faces) == 0 {
		return face{}, false, err
	}
	best := faces[0]
	for _, f := range faces[1:] {
		if f.area() > best.area() {
			best = f
		}
	}
	if withYaw {
		if best.yaw, err = e.yaw(ctx, src, best.box); err != nil {
			return face{}, false, err
		}
	}
	if withEmb {
		if best.emb, err = e.embed(ctx, src, best.kps); err != nil {
			return face{}, false, err
		}
	}
	for k := range best.kps {
		best.kps[k][0] -= float64(pad)
		best.kps[k][1] -= float64(pad)
	}
	for k := range best.box {
		best.box[k] -= float64(pad)
	}
	return best, true, nil
}
