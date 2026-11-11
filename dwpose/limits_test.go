package dwpose

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pngHeaderOnly(t *testing.T, w, h uint32) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	b := buf.Bytes()
	binary.BigEndian.PutUint32(b[16:], w)
	binary.BigEndian.PutUint32(b[20:], h)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return base64.StdEncoding.EncodeToString(b)
}

func TestHandlerRejectsOversized(t *testing.T) {
	for _, tc := range []struct {
		name string
		img  string
		res  int
	}{
		{"huge source header", pngHeaderOnly(t, 40000, 40000), 512},
		{"source over 16 MP", pngHeaderOnly(t, 5000, 4000), 512},
		{"huge processor res", grayB64(t, 8, 8), 60000},
		{"thin image blows up canvas", grayB64(t, 1, 4096), 512},
	} {
		t.Run(tc.name, func(t *testing.T) {
			est := &fakeEstimator{}
			rec := serve(NewHandler(est), "/controlnet/detect", detectBody(t, moduleName, tc.res, tc.img))
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Zero(t, est.calls())
		})
	}
	rec := serve(NewHandler(&fakeEstimator{}), "/controlnet/detect", detectBody(t, moduleName, 1024, grayB64(t, 832, 1216)))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestLibCandidatesNoRelativePaths(t *testing.T) {
	for _, p := range libCandidates("") {
		if filepath.IsAbs(p) {
			continue
		}
		assert.Equal(t, p, filepath.Base(p), "candidate %q must be absolute or a bare name for the system loader", p)
		assert.NotContains(t, p, "lib/")
	}
	assert.Equal(t, []string{"/x/libonnxruntime.so"}, libCandidates("/x/libonnxruntime.so"))
}

type blockingEstimator struct {
	active, peak atomic.Int32
	release      chan struct{}
}

func (b *blockingEstimator) Estimate(_ context.Context, img *image.RGBA) ([]Person, error) {
	n := b.active.Add(1)
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-b.release
	b.active.Add(-1)
	return nil, nil
}

func TestHandlerOneDetectionAtATime(t *testing.T) {
	est := &blockingEstimator{release: make(chan struct{})}
	h := NewHandler(est)
	body := detectBody(t, moduleName, 512, grayB64(t, 64, 64))
	done := make(chan int, 3)
	for i := 0; i < 3; i++ {
		go func() { done <- serve(h, "/controlnet/detect", body).Code }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for est.active.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), est.active.Load())
	for i := 0; i < 3; i++ {
		est.release <- struct{}{}
		assert.Equal(t, http.StatusOK, <-done)
	}
	assert.Equal(t, int32(1), est.peak.Load())
}
