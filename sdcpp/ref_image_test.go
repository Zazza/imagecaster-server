package sdcpp

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImg2ImgRefImageReachesSDCpp(t *testing.T) {
	cases := []struct {
		name     string
		withRef  bool
		wantRefs func(init, ref string) []any
	}{
		{"klein with ref_image sends init then ref", true, func(init, ref string) []any { return []any{init, ref} }},
		{"klein without ref_image sends only init", false, func(init, _ string) []any { return []any{init} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			init, ref := pngB64(t, 8, 8), pngB64(t, 6, 6)
			p := map[string]any{"prompt": "try on", "seed": 3, "init_image": init, "mask": ""}
			if tc.withRef {
				p["ref_image"] = ref
			}
			m := model("sdxl_base.safetensors")
			m["klein_reference"] = true
			st, imgs, _ := e.run(t, jobBody("img2img", m, p))
			require.Equal(t, statusCompleted, st["status"], st)
			require.Len(t, imgs, 1)
			bodies := e.sd.imgGenBodies()
			require.Len(t, bodies, 1)
			assert.Equal(t, init, bodies[0]["init_image"])
			assert.Equal(t, tc.wantRefs(init, ref), bodies[0]["ref_images"])
		})
	}
}

func TestImg2ImgRefImageRejected(t *testing.T) {
	cases := []struct {
		name     string
		kleinRef bool
		ref      string
	}{
		{"ref_image without klein_reference", false, ""},
		{"broken ref_image", true, "bm90IGFuIGltYWdl"},
		{"ref_image that is not base64", true, "%%%not-base64%%%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ref := tc.ref
			if ref == "" {
				ref = pngB64(t, 6, 6)
			}
			m := model("sdxl_base.safetensors")
			m["klein_reference"] = tc.kleinRef
			p := map[string]any{"prompt": "try on", "init_image": pngB64(t, 8, 8), "ref_image": ref}
			rec := e.do(t, http.MethodPost, "/jobs", jobBody("img2img", m, p))
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_params", sub(t, decodeMap(t, rec), "error")["code"])
			assert.Empty(t, e.sd.imgGenBodies())
		})
	}
}

func TestQueuedRefImageLivesOnDisk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sdjobs")
	e := newEnv(t, func(c *Config) { c.JobsDir = dir })
	leaser, open := gatedLeaser()
	e.f.SetLeaser(leaser)

	init, ref := pngB64(t, 8, 8), pngB64(t, 6, 6)
	m := model("sdxl_base.safetensors")
	m["klein_reference"] = true
	code, st := e.submit(t, jobBody("img2img", m, map[string]any{"prompt": "try on", "init_image": init, "ref_image": ref}))
	require.Equal(t, http.StatusAccepted, code, st)
	id := st["id"].(string)

	assert.FileExists(t, filepath.Join(dir, id, inputPrefix+"init_image"))
	assert.FileExists(t, filepath.Join(dir, id, inputPrefix+"ref_image"))

	close(open)
	st = e.wait(t, id)
	require.Equal(t, statusCompleted, st["status"], st)
	assert.NoFileExists(t, filepath.Join(dir, id, inputPrefix+"ref_image"))
	bodies := e.sd.imgGenBodies()
	require.Len(t, bodies, 1)
	assert.Equal(t, []any{init, ref}, bodies[0]["ref_images"])
}

func TestRemoveStaleInputs_IncludesRefImage(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"init_image", "mask", "image", "ref_image"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, inputPrefix+name), []byte("x"), 0o600))
	}
	removeStaleInputs(dir)
	for _, name := range []string{"init_image", "mask", "image", "ref_image"} {
		_, err := os.Stat(filepath.Join(dir, inputPrefix+name))
		assert.True(t, os.IsNotExist(err), name)
	}
}
