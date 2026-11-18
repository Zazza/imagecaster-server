package faceswap

import (
	"context"
	"encoding/binary"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimilarityRecoversTransform(t *testing.T) {
	src := [5][2]float64{{10, 20}, {40, 22}, {25, 35}, {14, 50}, {38, 52}}
	cases := []struct {
		name         string
		scale, angle float64
		tx, ty       float64
	}{
		{"identity", 1, 0, 0, 0},
		{"scaled", 2.5, 0, 3, -7},
		{"rotated", 0.8, 0.6, 100, 50},
		{"upside down", 1.3, math.Pi, -20, 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := tc.scale*math.Cos(tc.angle), tc.scale*math.Sin(tc.angle)
			want := affine{a, -b, tc.tx, b, a, tc.ty}
			var dst [5][2]float64
			for i, p := range src {
				dst[i][0], dst[i][1] = want.apply(p[0], p[1])
			}
			got := similarity(src, dst)
			for i := range want {
				assert.InDelta(t, want[i], got[i], 1e-9, "element %d", i)
			}
			inv := got.invert()
			for _, p := range src {
				x, y := got.apply(p[0], p[1])
				bx, by := inv.apply(x, y)
				assert.InDelta(t, p[0], bx, 1e-9)
				assert.InDelta(t, p[1], by, 1e-9)
			}
		})
	}
}

func TestEstimateNormTemplates(t *testing.T) {
	for _, tc := range []struct {
		size  int
		shift float64
	}{{112, 0}, {128, 8}} {
		m := estimateNorm(arcfaceDst, tc.size)
		ratio := float64(tc.size) / 112
		if tc.size == 128 {
			ratio = 1
		}
		x, y := m.apply(arcfaceDst[2][0], arcfaceDst[2][1])
		assert.InDelta(t, arcfaceDst[2][0]*ratio+tc.shift, x, 1e-6)
		assert.InDelta(t, arcfaceDst[2][1]*ratio, y, 1e-6)
	}
}

func scrfdOutputs(size int) [][]float32 {
	outs := make([][]float32, 9)
	for idx, stride := range []int{8, 16, 32} {
		n := (size / stride) * (size / stride) * 2
		outs[idx] = make([]float32, n)
		outs[idx+3] = make([]float32, 4*n)
		outs[idx+6] = make([]float32, 10*n)
	}
	return outs
}

func putAnchor(outs [][]float32, idx, i int, score float32, d [4]float32, kps [10]float32) {
	outs[idx][i] = score
	copy(outs[idx+3][4*i:], d[:])
	copy(outs[idx+6][10*i:], kps[:])
}

func TestDecodeSCRFD(t *testing.T) {
	const size = 64
	outs := scrfdOutputs(size)
	w8 := size / 8
	cell := 2*w8 + 3
	putAnchor(outs, 0, 2*cell+1, 0.9, [4]float32{1, 2, 3, 4}, [10]float32{0.5, 0.25, 1, 1, 0, 0, -1, 2, 2, 2})
	putAnchor(outs, 0, 2*cell, 0.6, [4]float32{1, 2, 3, 4}, [10]float32{})
	putAnchor(outs, 1, 0, 0.7, [4]float32{0, 0, 1, 1}, [10]float32{})
	putAnchor(outs, 2, 3, 0.49, [4]float32{1, 1, 1, 1}, [10]float32{})

	faces, err := decodeSCRFD(outs, size, 0.5, 0.5)
	require.NoError(t, err)
	require.Len(t, faces, 2, "the overlapping 0.6 box must be suppressed, the 0.49 one dropped")

	cx, cy := 3*8.0, 2*8.0
	f := faces[0]
	assert.InDelta(t, 0.9, f.score, 1e-6)
	assert.Equal(t, [4]float64{(cx - 8) / 0.5, (cy - 16) / 0.5, (cx + 24) / 0.5, (cy + 32) / 0.5}, f.box)
	assert.Equal(t, [2]float64{(cx + 4) / 0.5, (cy + 2) / 0.5}, f.kps[0])
	assert.Equal(t, [2]float64{(cx - 8) / 0.5, (cy + 16) / 0.5}, f.kps[3])
	assert.InDelta(t, 0.7, faces[1].score, 1e-6)
	assert.Equal(t, [4]float64{0, 0, 32, 32}, faces[1].box)

	_, err = decodeSCRFD(outs[:6], size, 0.5, 1)
	assert.Error(t, err)
}

func TestYawFromLandmarks(t *testing.T) {
	for _, deg := range []float64{-70, -30, 0, 12, 45, 66} {
		th := deg * math.Pi / 180
		var pts [68][3]float64
		for i := 0; i < 68; i++ {
			x, y, z := meanShape68[3*i], meanShape68[3*i+1], meanShape68[3*i+2]
			pts[i] = [3]float64{
				90*(math.Cos(th)*x+math.Sin(th)*z) + 300,
				90*y + 200,
				90 * (-math.Sin(th)*x + math.Cos(th)*z),
			}
		}
		assert.InDelta(t, deg, yawFromLandmarks(pts), 1e-6, "yaw %v", deg)
	}
}

func pbVarint(v uint64) []byte { return binary.AppendUvarint(nil, v) }

func pbBytes(field int, b []byte) []byte {
	return append(append(pbVarint(uint64(field)<<3|2), pbVarint(uint64(len(b)))...), b...)
}

func pbInt(field int, v uint64) []byte { return append(pbVarint(uint64(field)<<3), pbVarint(v)...) }

func floatsLE(v ...float32) []byte {
	var out []byte
	for _, f := range v {
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(f))
	}
	return out
}

func tensor(name string, dtype uint64, body []byte) []byte {
	t := append(pbInt(1, 2), pbInt(2, dtype)...)
	t = append(t, pbBytes(8, []byte(name))...)
	return append(t, body...)
}

func TestLastInitializer(t *testing.T) {
	model := func(inits ...[]byte) []byte {
		graph := pbBytes(1, []byte("node"))
		for _, in := range inits {
			graph = append(graph, pbBytes(5, in)...)
		}
		return append(append(pbInt(1, 8), pbBytes(7, graph)...), pbBytes(8, []byte("opset"))...)
	}
	cases := []struct {
		name    string
		model   []byte
		want    []float32
		wantErr bool
	}{
		{"raw data", model(tensor("w", 1, pbBytes(9, floatsLE(9, 9))), tensor("emap", 1, pbBytes(9, floatsLE(1, -2, 3.5)))), []float32{1, -2, 3.5}, false},
		{"float data", model(tensor("emap", 1, pbBytes(4, floatsLE(0.25, 4)))), []float32{0.25, 4}, false},
		{"not float", model(tensor("emap", 7, pbBytes(9, floatsLE(1)))), nil, true},
		{"no graph", pbInt(1, 8), nil, true},
		{"truncated", model(tensor("emap", 1, pbBytes(9, floatsLE(1))))[:12], nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lastInitializer(tc.model)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMeanIdentity(t *testing.T) {
	vec := func(i int, scale float32) []float32 {
		v := make([]float32, embDim)
		v[i] = scale
		return v
	}
	id, err := meanIdentity([][]float32{vec(0, 3), vec(1, 0.5), vec(0, 10)})
	require.NoError(t, err)
	assert.Equal(t, IdentityModel, id.Model)
	assert.Equal(t, 3, id.PhotosUsed)
	want := normalize(append([]float32{2, 1}, make([]float32, embDim-2)...))
	for i := range want {
		assert.InDelta(t, want[i], id.Embedding[i], 1e-6)
	}
	_, err = meanIdentity([][]float32{vec(0, 1), vec(1, 1)})
	assert.ErrorIs(t, err, ErrTooFewFaces)
}

func TestAvailableNeedsAllModels(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{ModelsDir: dir, LibPath: filepath.Join(dir, "libonnxruntime.so")})
	assert.False(t, e.Available())
	for _, m := range Models {
		require.NoError(t, os.WriteFile(filepath.Join(dir, m), []byte("x"), 0o644))
	}
	assert.True(t, e.modelsPresent())
	assert.False(t, e.Available(), "no ONNX Runtime library")
	require.NoError(t, os.Remove(filepath.Join(dir, modelRestore)))
	assert.False(t, e.modelsPresent())
	assert.False(t, (*Engine)(nil).Available())
}

func TestSwapWithoutModels(t *testing.T) {
	e := New(Config{ModelsDir: t.TempDir()})
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	_, _, err := e.Swap(context.Background(), img, make([]float32, embDim), 0.6)
	assert.ErrorIs(t, err, ErrUnavailable)
	_, _, err = e.Swap(context.Background(), img, make([]float32, 10), 0.6)
	assert.Error(t, err)
	_, err = e.FromImages(context.Background(), []string{filepath.Join(t.TempDir(), "missing.png")})
	assert.ErrorIs(t, err, ErrTooFewFaces)
}

func TestMasks(t *testing.T) {
	k := gaussKernel(31)
	var sum float32
	for _, v := range k {
		sum += v
	}
	assert.InDelta(t, 1, sum, 1e-5)
	m := boxMask(swapSize)
	assert.InDelta(t, 1, m.pix[64*swapSize+64], 1e-4)
	assert.InDelta(t, 0, m.pix[0], 1e-4)

	p := newPlane(20, 20, 1)
	for y := 5; y < 15; y++ {
		for x := 5; x < 15; x++ {
			p.pix[y*20+x] = 1
		}
	}
	er := erode(p, 9)
	assert.Equal(t, float32(1), er.pix[10*20+9])
	assert.Equal(t, float32(0), er.pix[10*20+8])
}

func TestPasteBackFullMask(t *testing.T) {
	dst := newPlane(40, 40, 3)
	part := newPlane(16, 16, 3)
	for i := range part.pix {
		part.pix[i] = 200
	}
	mask := newPlane(16, 16, 1)
	for i := range mask.pix {
		mask.pix[i] = 1
	}
	m := affine{1, 0, -10, 0, 1, -12}
	pasteBack(dst, part, m, mask)
	assert.Equal(t, float32(200), dst.at(15, 18, 0))
	assert.Equal(t, float32(0), dst.at(2, 2, 0))
	assert.Equal(t, float32(0), dst.at(35, 35, 1))
}
