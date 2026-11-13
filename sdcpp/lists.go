package sdcpp

import (
	"bytes"
	"context"
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

func (f *Facade) checkpoints(w http.ResponseWriter) {
	out := []map[string]string{}
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
			out = append(out, map[string]string{"name": rel, "file": p})
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
		out = append(out, map[string]string{"name": k, "file": ""})
	}
	respondJSON(w, http.StatusOK, map[string]any{"checkpoints": out})
}

func (f *Facade) vaes(w http.ResponseWriter) {
	out := []map[string]string{}
	if f.cfg.VAEDir != "" {
		if ents, err := os.ReadDir(f.cfg.VAEDir); err == nil {
			for _, e := range ents {
				if e.IsDir() || !hasModelExt(e.Name()) {
					continue
				}
				out = append(out, map[string]string{"name": e.Name(), "file": filepath.Join(f.cfg.VAEDir, e.Name())})
			}
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"vaes": out})
}

func (f *Facade) loras(w http.ResponseWriter, r *http.Request) {
	list, err := f.fetchLoras(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"loras": list})
}

func (f *Facade) upscalers(w http.ResponseWriter) {
	out := []map[string]any{
		{"name": "lanczos", "scale": 4},
		{"name": "nearest", "scale": 4},
	}
	for _, p := range f.upscalerFiles() {
		out = append(out, map[string]any{"name": strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)), "scale": 4})
	}
	respondJSON(w, http.StatusOK, map[string]any{"upscalers": out})
}

func imageSizeB64(s string) (int, int, error) {
	if i := strings.Index(s, ","); i >= 0 && strings.HasPrefix(s, "data:") {
		s = s[i+1:]
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
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

func upscaleTarget(src image.Image, scale float64) (int, int, error) {
	b := src.Bounds()
	fw, fh := math.Round(float64(b.Dx())*scale), math.Round(float64(b.Dy())*scale)
	if !(fw >= 1 && fh >= 1 && fw <= maxSide && fh <= maxSide && fw*fh <= maxPixels) {
		return 0, 0, apiErr(http.StatusBadRequest, "invalid_params", "bad scale %v: result must be 1..%d px per side and at most %d MP", scale, maxSide, maxPixels/1_000_000)
	}
	return int(fw), int(fh), nil
}

func upscaleScale(p GenParams) float64 {
	if p.Scale != nil && *p.Scale > 0 {
		return *p.Scale
	}
	return 2
}

func (f *Facade) checkUpscale(p GenParams) error {
	if p.Image == "" {
		return apiErr(http.StatusBadRequest, "invalid_params", "upscale needs params.image")
	}
	src, err := decodeImageB64(p.Image)
	if err != nil {
		return apiErr(http.StatusBadRequest, "invalid_params", "decode image: %v", err)
	}
	if _, _, err := upscaleTarget(src, upscaleScale(p)); err != nil {
		return err
	}
	_, err = f.resolveUpscaler(p.Upscaler, false)
	return err
}

func (f *Facade) upscale(ctx context.Context, p GenParams) ([]string, map[string]any, error) {
	src, err := decodeImageB64(p.Image)
	if err != nil {
		return nil, nil, apiErr(http.StatusBadRequest, "invalid_params", "decode image: %v", err)
	}
	scale := upscaleScale(p)
	tw, th, err := upscaleTarget(src, scale)
	if err != nil {
		return nil, nil, err
	}
	name, err := f.resolveUpscaler(p.Upscaler, false)
	if err != nil {
		return nil, nil, err
	}
	var out image.Image
	switch name {
	case "Nearest":
		out = resizeTo(src, tw, th, xdraw.NearestNeighbor)
	case "Lanczos":
		out = resizeTo(src, tw, th, xdraw.CatmullRom)
	default:
		repeats := 1
		if scale > 4 {
			repeats = 2
		}
		code, body, derr := f.do(ctx, http.MethodPost, "/sdcpp/v1/upscale", map[string]any{"image": p.Image, "upscaler": name, "repeats": repeats}, 10*time.Minute)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if derr != nil {
			return nil, nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable: %v", derr)
		}
		if code != http.StatusOK {
			return nil, nil, submitError(code, body)
		}
		var resp struct {
			Images []struct {
				B64 string `json:"b64_json"`
			} `json:"images"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || len(resp.Images) == 0 {
			return nil, nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server upscale: bad response: %s", truncate(body, 200))
		}
		up, err := decodeImageB64(resp.Images[0].B64)
		if err != nil {
			return nil, nil, apiErr(http.StatusBadGateway, "sd_failed", "decode upscaled image: %v", err)
		}
		out = resizeTo(up, tw, th, xdraw.CatmullRom)
	}
	enc, err := encodePNG(out)
	if err != nil {
		return nil, nil, apiErr(http.StatusInternalServerError, "sd_failed", "encode png: %v", err)
	}
	info := map[string]any{"upscaler": p.Upscaler, "scale": scale, "width": tw, "height": th, "backend": "sd.cpp"}
	return []string{enc}, info, nil
}
