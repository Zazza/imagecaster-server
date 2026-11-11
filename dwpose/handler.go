package dwpose

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net/http"
	"strings"

	_ "image/jpeg"
	_ "image/png"
)

const moduleName = "dw_openpose_full"

type detectRequest struct {
	Module string   `json:"controlnet_module"`
	Images []string `json:"controlnet_input_images"`
	Res    int      `json:"controlnet_processor_res"`
}

type poseJSON struct {
	People       []PersonJSON `json:"people"`
	Animals      []any        `json:"animals"`
	CanvasWidth  int          `json:"canvas_width"`
	CanvasHeight int          `json:"canvas_height"`
}

func NewHandler(est Estimator) http.Handler {
	busy := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			respond(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
			return
		}
		var req detectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Module != moduleName {
			fail(w, http.StatusBadRequest, "module "+req.Module+" is not supported, only "+moduleName)
			return
		}
		if len(req.Images) == 0 {
			fail(w, http.StatusBadRequest, "controlnet_input_images is empty")
			return
		}
		res := req.Res
		if res <= 0 {
			res = 512
		}
		if res > maxSide {
			fail(w, http.StatusBadRequest, fmt.Sprintf("controlnet_processor_res %d exceeds %d", res, maxSide))
			return
		}
		select {
		case busy <- struct{}{}:
		case <-r.Context().Done():
			fail(w, http.StatusServiceUnavailable, "request cancelled while waiting for another pose detection")
			return
		}
		defer func() { <-busy }()
		poses := make([]poseJSON, 0, len(req.Images))
		for _, s := range req.Images {
			img, err := decodeB64(s)
			if err != nil {
				fail(w, http.StatusBadRequest, "decode image: "+err.Error())
				return
			}
			b := img.Bounds()
			if _, _, cw, ch := CanvasSize(b.Dx(), b.Dy(), res); !sizeAllowed(cw, ch) {
				fail(w, http.StatusBadRequest, fmt.Sprintf("canvas %dx%d exceeds %d px per side or %d MP", cw, ch, maxSide, maxPixels/1_000_000))
				return
			}
			canvas := fitCanvas(toRGB(img), res)
			people, err := est.Estimate(r.Context(), canvas)
			if err != nil {
				code := http.StatusInternalServerError
				if errors.Is(err, ErrUnavailable) {
					code = http.StatusServiceUnavailable
				}
				fail(w, code, err.Error())
				return
			}
			pj := make([]PersonJSON, 0, len(people))
			for _, p := range people {
				pj = append(pj, Format(p))
			}
			cb := canvas.Bounds()
			poses = append(poses, poseJSON{People: pj, Animals: []any{}, CanvasWidth: cb.Dx(), CanvasHeight: cb.Dy()})
		}
		respond(w, http.StatusOK, map[string]any{"images": []string{}, "info": "Success", "poses": poses})
	})
}

func decodeB64(s string) (image.Image, error) {
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
	maxPixels = 16_000_000
)

func sizeAllowed(w, h int) bool {
	return w > 0 && h > 0 && w <= maxSide && h <= maxSide && int64(w)*int64(h) <= maxPixels
}

func fail(w http.ResponseWriter, code int, msg string) {
	respond(w, code, map[string]string{"error": msg, "detail": msg})
}

func respond(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}
