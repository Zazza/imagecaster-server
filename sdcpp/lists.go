package sdcpp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "image/jpeg"

	xdraw "golang.org/x/image/draw"
)

func (f *Facade) sdModels(w http.ResponseWriter) {
	out := []map[string]any{}
	seen := map[string]bool{}
	if f.cfg.ModelsDir != "" {
		filepath.WalkDir(f.cfg.ModelsDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !hasModelExt(d.Name()) {
				return nil
			}
			rel, rerr := filepath.Rel(f.cfg.ModelsDir, p)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			seen[rel] = true
			seen[stem(rel)] = true
			out = append(out, map[string]any{
				"title":      rel,
				"model_name": strings.TrimSuffix(rel, filepath.Ext(rel)),
				"filename":   p,
				"hash":       nil,
				"sha256":     nil,
				"config":     nil,
			})
			return nil
		})
	}
	keys := make([]string, 0, len(f.cfg.Aliases))
	for k := range f.cfg.Aliases {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if seen[k] || seen[stem(k)] {
			continue
		}
		out = append(out, map[string]any{
			"title":      k,
			"model_name": stem(k),
			"filename":   "",
			"hash":       nil,
			"sha256":     nil,
			"config":     nil,
		})
	}
	respondJSON(w, http.StatusOK, out)
}

func (f *Facade) sdVAE(w http.ResponseWriter) {
	out := []map[string]any{}
	if f.cfg.VAEDir != "" {
		if ents, err := os.ReadDir(f.cfg.VAEDir); err == nil {
			for _, e := range ents {
				if e.IsDir() || !hasModelExt(e.Name()) {
					continue
				}
				out = append(out, map[string]any{"model_name": e.Name(), "filename": filepath.Join(f.cfg.VAEDir, e.Name())})
			}
		}
	}
	respondJSON(w, http.StatusOK, out)
}

func (f *Facade) loras(w http.ResponseWriter, r *http.Request) {
	list, err := f.fetchLoras(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, l := range list {
		out = append(out, map[string]any{"name": l.Name, "alias": l.Name, "path": l.Path, "metadata": map[string]any{}})
	}
	respondJSON(w, http.StatusOK, out)
}

func (f *Facade) samplers(w http.ResponseWriter) {
	out := make([]map[string]any, 0, len(samplers))
	for _, s := range samplers {
		out = append(out, map[string]any{"name": s.forge, "aliases": []string{s.sdcpp}, "options": map[string]any{}})
	}
	respondJSON(w, http.StatusOK, out)
}

func (f *Facade) schedulers(w http.ResponseWriter) {
	out := make([]map[string]any, 0, len(schedulers)+1)
	out = append(out, map[string]any{"name": "Automatic", "label": "Automatic", "aliases": nil})
	for _, s := range schedulers {
		out = append(out, map[string]any{"name": s.forge, "label": s.forge, "aliases": nil})
	}
	respondJSON(w, http.StatusOK, out)
}

func (f *Facade) upscalers(w http.ResponseWriter) {
	out := []map[string]any{
		{"name": "None", "model_name": nil, "model_path": nil, "scale": 4},
		{"name": "Lanczos", "model_name": nil, "model_path": nil, "scale": 4},
		{"name": "Nearest", "model_name": nil, "model_path": nil, "scale": 4},
	}
	rev := map[string]string{}
	for forge, s := range upscalerAliases {
		rev[s] = forge
	}
	for _, p := range f.upscalerFiles() {
		s := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		out = append(out, map[string]any{"name": s, "model_name": s, "model_path": p, "scale": 4})
		if forge, ok := rev[s]; ok {
			out = append(out, map[string]any{"name": forge, "model_name": s, "model_path": p, "scale": 4})
		}
	}
	respondJSON(w, http.StatusOK, out)
}

func decodeImageB64(s string) (image.Image, error) {
	if i := strings.Index(s, ","); i >= 0 && strings.HasPrefix(s, "data:") {
		s = s[i+1:]
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if !sizeAllowed(cfg.Width, cfg.Height) {
		return nil, fmt.Errorf("image %dx%d exceeds %d px per side or %d MP", cfg.Width, cfg.Height, maxSide, maxPixels/1_000_000)
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

const (
	maxSide   = 16384
	maxPixels = 64_000_000
)

func sizeAllowed(w, h int) bool {
	return w > 0 && h > 0 && w <= maxSide && h <= maxSide && int64(w)*int64(h) <= maxPixels
}

func encodePNG(img image.Image) (string, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func resizeTo(img image.Image, w, h int, k xdraw.Interpolator) image.Image {
	b := img.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	k.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

func (f *Facade) extraSingle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image    string   `json:"image"`
		Upscaler string   `json:"upscaler_1"`
		Resize   *float64 `json:"upscaling_resize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errf(http.StatusBadRequest, "invalid JSON: %v", err))
		return
	}
	src, err := decodeImageB64(req.Image)
	if err != nil {
		writeErr(w, errf(http.StatusBadRequest, "decode image: %v", err))
		return
	}
	scale := 2.0
	if req.Resize != nil && *req.Resize > 0 {
		scale = *req.Resize
	}
	b := src.Bounds()
	fw, fh := math.Round(float64(b.Dx())*scale), math.Round(float64(b.Dy())*scale)
	if !(fw >= 1 && fh >= 1 && fw <= maxSide && fh <= maxSide && fw*fh <= maxPixels) {
		writeErr(w, errf(http.StatusBadRequest, "bad upscaling_resize %v: result must be 1..%d px per side and at most %d MP", scale, maxSide, maxPixels/1_000_000))
		return
	}
	tw, th := int(fw), int(fh)
	name, err := f.resolveUpscaler(req.Upscaler, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var out image.Image
	switch name {
	case "Nearest":
		out = resizeTo(src, tw, th, xdraw.NearestNeighbor)
	case "None", "Lanczos":
		out = resizeTo(src, tw, th, xdraw.CatmullRom)
	default:
		repeats := 1
		if scale > 4 {
			repeats = 2
		}
		f.gen.Lock()
		code, body, derr := f.do(r.Context(), http.MethodPost, "/sdcpp/v1/upscale", map[string]any{"image": req.Image, "upscaler": name, "repeats": repeats}, 10*time.Minute)
		f.gen.Unlock()
		if derr != nil {
			writeErr(w, errf(http.StatusBadGateway, "sd-server unavailable: %v", derr))
			return
		}
		if code != http.StatusOK {
			writeErr(w, submitError(code, body))
			return
		}
		var resp struct {
			Images []struct {
				B64 string `json:"b64_json"`
			} `json:"images"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || len(resp.Images) == 0 {
			writeErr(w, errf(http.StatusBadGateway, "sd-server upscale: bad response: %s", truncate(body, 200)))
			return
		}
		up, err := decodeImageB64(resp.Images[0].B64)
		if err != nil {
			writeErr(w, errf(http.StatusBadGateway, "decode upscaled image: %v", err))
			return
		}
		out = resizeTo(up, tw, th, xdraw.CatmullRom)
	}
	enc, err := encodePNG(out)
	if err != nil {
		writeErr(w, errf(http.StatusInternalServerError, "encode png: %v", err))
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"image": enc, "html_info": ""})
}
