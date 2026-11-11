package sdcpp

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pngWithHeader(t *testing.T, w, h uint32) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	b := buf.Bytes()
	binary.BigEndian.PutUint32(b[16:], w)
	binary.BigEndian.PutUint32(b[20:], h)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return base64.StdEncoding.EncodeToString(b)
}

func TestExtraSingle_RejectsHugeSource(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/extra-single-image", map[string]any{
		"image": pngWithHeader(t, 40000, 40000), "upscaler_1": "Lanczos", "upscaling_resize": 1,
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestExtraSingle_RejectsHugeResult(t *testing.T) {
	e := newEnv(t)
	for _, scale := range []float64{1000, 1e300} {
		rec := e.do(t, http.MethodPost, "/sdapi/v1/extra-single-image", map[string]any{
			"image": pngB64(t, 64, 64), "upscaler_1": "Lanczos", "upscaling_resize": scale,
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "scale %v: %s", scale, rec.Body.String())
	}
	rec := e.do(t, http.MethodPost, "/sdapi/v1/extra-single-image", map[string]any{
		"image": pngB64(t, 64, 64), "upscaler_1": "Lanczos", "upscaling_resize": 4,
	})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestModelPathsStayInsideFolders(t *testing.T) {
	d := newModelDirs(t)
	outside := touch(t, filepath.Join(t.TempDir(), "evil.gguf"))
	te := touch(t, filepath.Join(d.cfg.TextEncoderDir, "qwen_3_4b.safetensors"))
	vae := touch(t, filepath.Join(d.cfg.VAEDir, "flux2-vae.safetensors"))
	touch(t, filepath.Join(d.cfg.ModelsDir, "flux-2-klein-4b.safetensors"))
	for _, tc := range []struct {
		name, ckpt string
		mods       []string
	}{
		{"absolute checkpoint outside", outside, nil},
		{"relative checkpoint escaping", "../../" + filepath.Base(filepath.Dir(outside)) + "/evil.gguf", nil},
		{"module outside", "flux-2-klein-4b.safetensors", []string{outside, vae}},
		{"module escaping", "flux-2-klein-4b.safetensors", []string{"../../evil.gguf", vae}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ModelArgs(d.cfg, tc.ckpt, tc.mods)
			assert.Error(t, err)
		})
	}
	args, err := ModelArgs(d.cfg, "flux-2-klein-4b.safetensors", []string{te, vae})
	require.NoError(t, err)
	assert.Contains(t, args, te)
}

func TestOptionsRejectsVAEOutside(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors", "sd_vae": "../../../etc/x.safetensors"})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, e.rs.Calls())
}
