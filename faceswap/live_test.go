//go:build faceswap_live

package faceswap

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pyRef struct {
	Frame    string  `json:"frame"`
	Skip     string  `json:"skip"`
	FacePx   int     `json:"face_px"`
	Yaw      float64 `json:"yaw"`
	SimAfter float64 `json:"sim_after"`
}

func loadIdentity(t *testing.T, path string) []float32 {
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	if strings.HasSuffix(path, ".json") {
		var v []float32
		require.NoError(t, json.Unmarshal(b, &v))
		return v
	}
	require.True(t, bytes.HasPrefix(b, []byte("\x93NUMPY")), "not a .npy file")
	hl, off := int(binary.LittleEndian.Uint16(b[8:])), 10
	if b[6] >= 2 {
		hl, off = int(binary.LittleEndian.Uint32(b[8:])), 12
	}
	header, data := string(b[off:off+hl]), b[off+hl:]
	var out []float32
	switch {
	case strings.Contains(header, "'<f4'"):
		for i := 0; i+4 <= len(data); i += 4 {
			out = append(out, math.Float32frombits(binary.LittleEndian.Uint32(data[i:])))
		}
	case strings.Contains(header, "'<f8'"):
		for i := 0; i+8 <= len(data); i += 8 {
			out = append(out, float32(math.Float64frombits(binary.LittleEndian.Uint64(data[i:]))))
		}
	default:
		t.Fatalf("unsupported npy header %s", header)
	}
	return out
}

func TestLiveMatchesPython(t *testing.T) {
	models, lib, frames, idPath := os.Getenv("FACESWAP_MODELS"), os.Getenv("FACESWAP_ORT"), os.Getenv("FACESWAP_FRAMES"), os.Getenv("FACESWAP_IDENTITY")
	if models == "" || lib == "" || frames == "" || idPath == "" {
		t.Skip("set FACESWAP_MODELS, FACESWAP_ORT, FACESWAP_FRAMES and FACESWAP_IDENTITY")
	}
	outDir := os.Getenv("FACESWAP_OUT")
	id := loadIdentity(t, idPath)
	require.Len(t, id, embDim)
	raw, err := os.ReadFile(filepath.Join("testdata", "python_ref.json"))
	require.NoError(t, err)
	var refs []pyRef
	require.NoError(t, json.Unmarshal(raw, &refs))

	e := New(Config{LibPath: lib, ModelsDir: models})
	require.True(t, e.Available())
	ctx := context.Background()
	saved := 0
	var total time.Duration
	swapped := 0
	for _, ref := range refs {
		t.Run(ref.Frame, func(t *testing.T) {
			fh, err := os.Open(filepath.Join(frames, ref.Frame+".png"))
			require.NoError(t, err)
			img, _, err := image.Decode(fh)
			fh.Close()
			require.NoError(t, err)
			start := time.Now()
			out, rep, err := e.Swap(ctx, img, id, 0.6)
			took := time.Since(start)
			require.NoError(t, err)
			require.Equal(t, ref.Skip, rep.Skip)
			if ref.Skip != "" {
				return
			}
			total += took
			swapped++
			g, ok, err := e.largestFace(ctx, fromImage(out), false, true)
			require.NoError(t, err)
			require.True(t, ok, "no face after swap")
			sim := Similarity(g.emb, id)
			t.Logf("%s: face_px %d/%d yaw %d/%.1f sim %.3f/%.3f (%.2fs)", ref.Frame, rep.FacePx, ref.FacePx, rep.Yaw, ref.Yaw, sim, ref.SimAfter, took.Seconds())
			assert.InDelta(t, ref.FacePx, rep.FacePx, 1)
			assert.InDelta(t, math.Round(ref.Yaw), rep.Yaw, 1)
			assert.InDelta(t, ref.SimAfter, sim, 0.03)
			if outDir != "" && saved < 3 {
				saved++
				var buf bytes.Buffer
				require.NoError(t, png.Encode(&buf, out))
				require.NoError(t, os.WriteFile(filepath.Join(outDir, ref.Frame+"_go.png"), buf.Bytes(), 0o644))
			}
		})
	}
	if swapped > 0 {
		t.Logf("swapped %d frames, %.2fs per frame", swapped, total.Seconds()/float64(swapped))
	}
}

func TestLiveIdentityFromFrames(t *testing.T) {
	models, lib, frames := os.Getenv("FACESWAP_MODELS"), os.Getenv("FACESWAP_ORT"), os.Getenv("FACESWAP_FRAMES")
	if models == "" || lib == "" || frames == "" {
		t.Skip("set FACESWAP_MODELS, FACESWAP_ORT and FACESWAP_FRAMES")
	}
	var paths []string
	for _, f := range []string{"curated_s1_1001", "curated_s2_1001", "curated_s2_2002", "curated_s2_3003"} {
		paths = append(paths, filepath.Join(frames, f+".png"))
	}
	e := New(Config{LibPath: lib, ModelsDir: models})
	id, err := e.FromImages(context.Background(), paths)
	require.NoError(t, err)
	assert.Equal(t, len(paths)-1, id.PhotosUsed, "the frame without a face is skipped")
	assert.Len(t, id.Embedding, embDim)
	assert.InDelta(t, 1, Similarity(id.Embedding, id.Embedding), 1e-6)
	_, err = e.FromImages(context.Background(), paths[len(paths)-2:])
	assert.ErrorIs(t, err, ErrTooFewFaces)
}
