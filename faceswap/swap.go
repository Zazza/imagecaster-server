package faceswap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"

	"imagecaster-server/onnxrt"
)

const (
	swapSize   = 128
	MinFacePx  = 60
	MaxYaw     = 65
	SkipNoFace = "no_face"
	SkipSmall  = "face_small"
	SkipYaw    = "yaw"
)

type Report struct {
	Skip   string
	FacePx int
	Yaw    int
}

func (e *Engine) Swap(ctx context.Context, img image.Image, id []float32, blend float64) (image.Image, Report, error) {
	if len(id) != embDim {
		return nil, Report{}, fmt.Errorf("identity has %d values, want %d", len(id), embDim)
	}
	unlock := e.lock()
	defer unlock()
	src := fromImage(img)
	f, ok, err := e.largestFace(ctx, src, true, false)
	if err != nil {
		return nil, Report{}, err
	}
	if !ok {
		return img, Report{Skip: SkipNoFace}, nil
	}
	fh := f.height()
	rep := Report{FacePx: int(fh), Yaw: int(math.Round(f.yaw))}
	switch {
	case fh < MinFacePx:
		rep.Skip = SkipSmall
	case math.Abs(f.yaw) > MaxYaw:
		rep.Skip = SkipYaw
	}
	if rep.Skip != "" {
		return img, rep, nil
	}
	out, err := e.swapFace(ctx, src, f.kps, id, blend)
	if err != nil {
		return nil, Report{}, err
	}
	return out.toImage(), rep, nil
}

func (e *Engine) swapFace(ctx context.Context, src plane, kps [5][2]float64, id []float32, blend float64) (plane, error) {
	s, err := e.session(modelSwap)
	if err != nil {
		return plane{}, err
	}
	m := estimateNorm(kps, swapSize)
	crop := warp(src.constant(0), 3, m, swapSize, swapSize, true)
	outs, _, err := onnxrt.Run(ctx, s,
		onnxrt.Input{Name: "target", Shape: []int64{1, 3, swapSize, swapSize}, Data: chw(crop, func(v float32, _ int) float32 { return v / 255 })},
		onnxrt.Input{Name: "source", Shape: []int64{1, embDim}, Data: e.latent(id)},
	)
	if err != nil {
		return plane{}, fmt.Errorf("inswapper: %w", err)
	}
	if len(outs[0]) != 3*swapSize*swapSize {
		return plane{}, fmt.Errorf("inswapper: %d values", len(outs[0]))
	}
	part := fromCHW(outs[0], swapSize, swapSize, func(v float32) float32 { return trunc8(min(max(v, 0), 1) * 255) })
	step := plane{w: src.w, h: src.h, c: 3, pix: append([]float32(nil), src.pix...)}
	pasteBack(step, part, m, boxMask(swapSize))

	m512 := similarity(kps, ffhq512)
	crop512 := warp(step.replicate, 3, m512, restoreSize, restoreSize, true)
	orig512 := warp(src.replicate, 3, m512, restoreSize, restoreSize, true)
	restored, err := e.restore(ctx, crop512)
	if err != nil {
		return plane{}, err
	}
	b := float32(blend)
	for i, v := range restored.pix {
		restored.pix[i] = round8(v*b + crop512.pix[i]*(1-b))
	}
	mask, err := e.parseMask(ctx, orig512)
	if err != nil {
		return plane{}, err
	}
	out := plane{w: src.w, h: src.h, c: 3, pix: append([]float32(nil), src.pix...)}
	pasteBack(out, restored, m512, mask)
	return out, nil
}

func (e *Engine) latent(id []float32) []float32 {
	id = normalize(id)
	out := make([]float32, embDim)
	for j := 0; j < embDim; j++ {
		var s float64
		for i := 0; i < embDim; i++ {
			s += float64(id[i]) * float64(e.emap[i*embDim+j])
		}
		out[j] = float32(s)
	}
	return normalize(out)
}

var errProto = errors.New("onnx: malformed protobuf")

func protoFields(b []byte, visit func(num, wire int, val []byte)) error {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return errProto
		}
		b = b[n:]
		num, wire := int(tag>>3), int(tag&7)
		var size int
		switch wire {
		case 0:
			_, n := binary.Uvarint(b)
			if n <= 0 {
				return errProto
			}
			size = n
		case 1:
			size = 8
		case 5:
			size = 4
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return errProto
			}
			b = b[n:]
			size = int(l)
		default:
			return errProto
		}
		if size > len(b) {
			return errProto
		}
		visit(num, wire, b[:size])
		b = b[size:]
	}
	return nil
}

func lastField(b []byte, num int) ([]byte, error) {
	var last []byte
	err := protoFields(b, func(n, wire int, v []byte) {
		if n == num && wire == 2 {
			last = v
		}
	})
	if err == nil && last == nil {
		err = fmt.Errorf("onnx: field %d not found", num)
	}
	return last, err
}

func lastInitializer(model []byte) ([]float32, error) {
	graph, err := lastField(model, 7)
	if err != nil {
		return nil, err
	}
	tensor, err := lastField(graph, 5)
	if err != nil {
		return nil, err
	}
	dtype := uint64(0)
	var raw []byte
	var floats []float32
	err = protoFields(tensor, func(n, wire int, v []byte) {
		switch {
		case n == 2 && wire == 0:
			dtype, _ = binary.Uvarint(v)
		case n == 9 && wire == 2:
			raw = v
		case n == 4 && wire == 2:
			for i := 0; i+4 <= len(v); i += 4 {
				floats = append(floats, math.Float32frombits(binary.LittleEndian.Uint32(v[i:])))
			}
		case n == 4 && wire == 5:
			floats = append(floats, math.Float32frombits(binary.LittleEndian.Uint32(v)))
		}
	})
	if err != nil {
		return nil, err
	}
	if dtype != 1 {
		return nil, fmt.Errorf("onnx: last initializer has data type %d, want float", dtype)
	}
	for i := 0; i+4 <= len(raw); i += 4 {
		floats = append(floats, math.Float32frombits(binary.LittleEndian.Uint32(raw[i:])))
	}
	return floats, nil
}
