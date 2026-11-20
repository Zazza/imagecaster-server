package train

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"net/http"

	"imagecaster-server/faceswap"
	"imagecaster-server/imgb64"
)

type FaceAnalyzer interface {
	Available() bool
	AnalyzeFaces(ctx context.Context, img image.Image, refs [][]float32) ([]faceswap.FaceInfo, error)
}

const (
	maxFacesBody = 24 << 20
	maxFaceRefs  = 100
)

var faceLimits = imgb64.Limits{MaxSide: 4096, MaxPixels: 16_000_000}

type facesRequest struct {
	ImageBase64 string      `json:"image_base64"`
	Refs        [][]float32 `json:"refs"`
}

func (m *Manager) handleFaces(w http.ResponseWriter, r *http.Request) {
	a, _ := m.identityBuilder().(FaceAnalyzer)
	if a == nil || !a.Available() {
		writeError(w, errf(http.StatusServiceUnavailable, "face_models_missing", "face models or ONNX Runtime are missing on the server"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFacesBody)
	var req facesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errf(http.StatusBadRequest, "invalid_request", "invalid JSON: %v", err))
		return
	}
	if len(req.Refs) > maxFaceRefs {
		req.Refs = req.Refs[:maxFaceRefs]
	}
	img, err := imgb64.Decode(req.ImageBase64, faceLimits)
	if err != nil {
		writeError(w, errf(http.StatusBadRequest, "invalid_image", "%v", err))
		return
	}
	faces, err := a.AnalyzeFaces(r.Context(), img, req.Refs)
	if errors.Is(err, faceswap.ErrUnavailable) {
		writeError(w, errf(http.StatusServiceUnavailable, "face_models_missing", "%v", err))
		return
	}
	if err != nil {
		writeError(w, errf(http.StatusInternalServerError, "internal", "%v", err))
		return
	}
	b := img.Bounds()
	writeJSON(w, http.StatusOK, map[string]any{"faces": faces, "width": b.Dx(), "height": b.Dy()})
}
