package sdcpp

import (
	"context"
	"errors"
	"image"
	"math"
	"net/http"
	"strings"

	"imagecaster-server/faceswap"
	"imagecaster-server/imgb64"
)

const defaultRestoreBlend = 0.6

var faceLimits = imgb64.Limits{MaxSide: 8192, MaxPixels: 16_777_216}

type FaceSwapper interface {
	Available() bool
	Swap(ctx context.Context, img image.Image, id []float32, blend float64) (image.Image, faceswap.Report, error)
}

func (f *Facade) SetFaceSwap(s FaceSwapper, identity func(person string) ([]float32, error)) {
	f.mu.Lock()
	f.faceSwap, f.identity = s, identity
	f.mu.Unlock()
}

func (f *Facade) faceDeps() (FaceSwapper, func(string) ([]float32, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.faceSwap, f.identity
}

func (f *Facade) personIdentity(person string) ([]float32, error) {
	_, identity := f.faceDeps()
	if identity == nil {
		return nil, apiErr(http.StatusServiceUnavailable, "face_models_missing", "face swap is not set up on this server")
	}
	id, err := identity(person)
	switch {
	case errors.Is(err, faceswap.ErrPersonNotFound):
		return nil, apiErr(http.StatusNotFound, "person_not_found", "person %q not found", person)
	case errors.Is(err, faceswap.ErrNoIdentity):
		return nil, apiErr(http.StatusConflict, "identity_missing", "person %q has no face identity yet", person)
	case err != nil:
		return nil, apiErr(http.StatusInternalServerError, "sd_failed", "read identity of %q: %v", person, err)
	}
	return id, nil
}

func restoreBlend(p GenParams) float64 {
	if p.RestoreBlend == nil {
		return defaultRestoreBlend
	}
	return *p.RestoreBlend
}

func (f *Facade) checkFaceSwap(p GenParams) error {
	if p.Image == "" {
		return apiErr(http.StatusBadRequest, "invalid_params", "face_swap needs params.image")
	}
	if _, _, err := imgb64.Check(p.Image, faceLimits); err != nil {
		return apiErr(http.StatusBadRequest, "invalid_params", "decode image: %v", err)
	}
	if b := restoreBlend(p); math.IsNaN(b) || b < 0 || b > 1 {
		return apiErr(http.StatusBadRequest, "invalid_params", "params.restore_blend must be within 0..1, got %v", b)
	}
	if strings.TrimSpace(p.Person) == "" {
		return apiErr(http.StatusBadRequest, "invalid_params", "face_swap needs params.person")
	}
	if fs, _ := f.faceDeps(); fs == nil || !fs.Available() {
		return apiErr(http.StatusServiceUnavailable, "face_models_missing", "face models or ONNX Runtime are missing on the server")
	}
	_, err := f.personIdentity(p.Person)
	return err
}

func (f *Facade) runFaceSwap(j *job, params GenParams) {
	ctx := j.ctx
	s := f.jobs
	select {
	case f.faceSem <- struct{}{}:
	case <-ctx.Done():
		f.finishErr(j, ctx.Err())
		return
	}
	defer func() { <-f.faceSem }()
	if err := s.loadInputs(j, &params); err != nil {
		f.finishErr(j, err)
		return
	}
	s.mu.Lock()
	started := j.st.Status == statusQueued
	if started {
		j.st.Status = statusRunning
		j.st.StartedAt = tptr(s.now())
	}
	s.mu.Unlock()
	if started {
		s.persist(j)
	}
	imgs, info, err := f.faceSwapImage(ctx, params)
	if err != nil || ctx.Err() != nil {
		f.finishErr(j, err)
		return
	}
	f.finish(j, statusCompleted, nil, &jobResult{Images: imgs, Info: info})
}

func (f *Facade) faceSwapImage(ctx context.Context, p GenParams) ([]string, map[string]any, error) {
	fs, _ := f.faceDeps()
	if fs == nil {
		return nil, nil, apiErr(http.StatusServiceUnavailable, "face_models_missing", "face swap is not set up on this server")
	}
	id, err := f.personIdentity(p.Person)
	if err != nil {
		return nil, nil, err
	}
	src, err := imgb64.Decode(p.Image, faceLimits)
	if err != nil {
		return nil, nil, apiErr(http.StatusBadRequest, "invalid_params", "decode image: %v", err)
	}
	out, rep, err := fs.Swap(ctx, src, id, restoreBlend(p))
	if errors.Is(err, faceswap.ErrUnavailable) {
		return nil, nil, apiErr(http.StatusServiceUnavailable, "face_models_missing", "%v", err)
	}
	if err != nil {
		return nil, nil, err
	}
	info := map[string]any{"face_swap": "done", "skip": rep.Skip, "face_px": rep.FacePx, "yaw": rep.Yaw}
	if rep.Skip != "" {
		info["face_swap"] = "skipped"
		return []string{}, info, nil
	}
	b64, err := encodePNG(out)
	if err != nil {
		return nil, nil, err
	}
	return []string{b64}, info, nil
}
