package sdcpp

import (
	"context"
	"errors"
	"image"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/faceswap"
)

type fakeSwapper struct {
	available bool
	report    faceswap.Report
	err       error
	calls     atomic.Int32
	gotBlend  atomic.Value
}

func (s *fakeSwapper) Available() bool { return s.available }

func (s *fakeSwapper) Swap(_ context.Context, img image.Image, id []float32, blend float64) (image.Image, faceswap.Report, error) {
	s.calls.Add(1)
	s.gotBlend.Store(blend)
	if s.err != nil {
		return nil, faceswap.Report{}, s.err
	}
	return img, s.report, nil
}

func identities(people map[string][]float32) func(string) ([]float32, error) {
	return func(name string) ([]float32, error) {
		id, ok := people[name]
		if !ok {
			return nil, faceswap.ErrPersonNotFound
		}
		if id == nil {
			return nil, faceswap.ErrNoIdentity
		}
		return id, nil
	}
}

func faceJob(params map[string]any) map[string]any {
	return map[string]any{"kind": "face_swap", "client": "app", "params": params}
}

func faceParams(person string, blend any) map[string]any {
	p := map[string]any{"image": jcPNG(16, 16), "person": person}
	if blend != nil {
		p["restore_blend"] = blend
	}
	return p
}

func newFaceEnv(t *testing.T, s *fakeSwapper) *jcEnv {
	e := newJcEnv(t)
	e.f.SetFaceSwap(s, identities(map[string][]float32{"anna": make([]float32, 512), "bob": nil}))
	return e
}

func TestFaceSwapValidation(t *testing.T) {
	cases := []struct {
		name     string
		swapper  *fakeSwapper
		params   map[string]any
		wantHTTP int
		wantCode string
	}{
		{"accepted", &fakeSwapper{available: true}, faceParams("anna", nil), http.StatusAccepted, ""},
		{"blend zero", &fakeSwapper{available: true}, faceParams("anna", 0.0), http.StatusAccepted, ""},
		{"blend one", &fakeSwapper{available: true}, faceParams("anna", 1.0), http.StatusAccepted, ""},
		{"blend above one", &fakeSwapper{available: true}, faceParams("anna", 1.5), http.StatusBadRequest, "invalid_params"},
		{"blend negative", &fakeSwapper{available: true}, faceParams("anna", -0.1), http.StatusBadRequest, "invalid_params"},
		{"no image", &fakeSwapper{available: true}, map[string]any{"person": "anna"}, http.StatusBadRequest, "invalid_params"},
		{"broken image", &fakeSwapper{available: true}, map[string]any{"person": "anna", "image": "bm90IGFuIGltYWdl"}, http.StatusBadRequest, "invalid_params"},
		{"no person", &fakeSwapper{available: true}, faceParams("", nil), http.StatusBadRequest, "invalid_params"},
		{"models missing", &fakeSwapper{available: false}, faceParams("anna", nil), http.StatusServiceUnavailable, "face_models_missing"},
		{"person not found", &fakeSwapper{available: true}, faceParams("zoe", nil), http.StatusNotFound, "person_not_found"},
		{"identity missing", &fakeSwapper{available: true}, faceParams("bob", nil), http.StatusConflict, "identity_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newFaceEnv(t, tc.swapper)
			rec := jcDo(t, e.f, http.MethodPost, "/jobs", faceJob(tc.params))
			require.Equal(t, tc.wantHTTP, rec.Code, rec.Body.String())
			if tc.wantCode != "" {
				assert.Equal(t, tc.wantCode, jcErrCode(t, rec))
			}
		})
	}
}

func TestFaceSwapNotWiredIsUnavailable(t *testing.T) {
	e := newJcEnv(t)
	rec := jcDo(t, e.f, http.MethodPost, "/jobs", faceJob(faceParams("anna", nil)))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "face_models_missing", jcErrCode(t, rec))
}

func TestFaceSwapResult(t *testing.T) {
	cases := []struct {
		name       string
		report     faceswap.Report
		wantImages int
		wantState  string
	}{
		{"done", faceswap.Report{FacePx: 150, Yaw: -3}, 1, "done"},
		{"no face", faceswap.Report{Skip: faceswap.SkipNoFace}, 0, "skipped"},
		{"small face", faceswap.Report{Skip: faceswap.SkipSmall, FacePx: 40, Yaw: 2}, 0, "skipped"},
		{"turned away", faceswap.Report{Skip: faceswap.SkipYaw, FacePx: 120, Yaw: 70}, 0, "skipped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeSwapper{available: true, report: tc.report}
			e := newFaceEnv(t, s)
			id := jcID(jcSubmit(t, e.f, faceJob(faceParams("anna", nil)), http.StatusAccepted))
			jcWaitStatus(t, e.f, id, statusCompleted)
			imgs, info := jcResult(t, e.f, id)
			assert.Len(t, imgs, tc.wantImages)
			assert.Equal(t, tc.wantState, info["face_swap"])
			assert.Equal(t, tc.report.Skip, info["skip"])
			assert.EqualValues(t, tc.report.FacePx, info["face_px"])
			assert.EqualValues(t, tc.report.Yaw, info["yaw"])
			assert.Equal(t, defaultRestoreBlend, s.gotBlend.Load())
		})
	}
}

func TestFaceSwapRunErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"models gone", faceswap.ErrUnavailable, "face_models_missing"},
		{"engine failure", errors.New("boom"), "sd_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newFaceEnv(t, &fakeSwapper{available: true, err: tc.err})
			id := jcID(jcSubmit(t, e.f, faceJob(faceParams("anna", 0.3)), http.StatusAccepted))
			st := jcWaitStatus(t, e.f, id, statusFailed)
			jerr, _ := st["error"].(map[string]any)
			assert.Equal(t, tc.wantCode, jerr["code"])
		})
	}
}

func TestFaceSwapDoesNotWaitForGPU(t *testing.T) {
	s := &fakeSwapper{available: true}
	e := newFaceEnv(t, s)
	var leases atomic.Int32
	e.f.SetLeaser(func(ctx context.Context, _ LeaseRequest) (func(), error) {
		leases.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	e.f.gen <- struct{}{}
	defer func() { <-e.f.gen }()
	id := jcID(jcSubmit(t, e.f, faceJob(faceParams("anna", nil)), http.StatusAccepted))
	jcWaitStatus(t, e.f, id, statusCompleted)
	assert.Zero(t, leases.Load())
	assert.EqualValues(t, 1, s.calls.Load())
}

func TestFaceSwapKeepsDefaultModelAlone(t *testing.T) {
	e := newFaceEnv(t, &fakeSwapper{available: true})
	body := faceJob(faceParams("anna", nil))
	body["model"] = map[string]any{"checkpoint": "", "vae": "", "modules": []string{}}
	id := jcID(jcSubmit(t, e.f, body, http.StatusAccepted))
	jcWaitStatus(t, e.f, id, statusCompleted)

	e.f.jobs.mu.Lock()
	j := e.f.jobs.jobs[id]
	model := j.model
	j.model = ModelSpec{Checkpoint: "sd15.safetensors"}
	j.st.Status = statusQueued
	_, ok := e.f.jobs.lastQueuedModelLocked()
	j.st.Status = statusCompleted
	e.f.jobs.mu.Unlock()
	assert.Equal(t, ModelSpec{}, model)
	assert.False(t, ok, "a face_swap job must not set the default model")

	rec := jcDo(t, e.f, http.MethodPost, "/jobs", jcTxt("app", "", "", "a cat"))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "model_not_found", jcErrCode(t, rec))
}

func TestFaceSwapSlotIsSerial(t *testing.T) {
	s := &fakeSwapper{available: true}
	e := newFaceEnv(t, s)
	e.f.faceSem <- struct{}{}
	id := jcID(jcSubmit(t, e.f, faceJob(faceParams("anna", nil)), http.StatusAccepted))
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, statusQueued, jcStatus(t, e.f, id)["status"])
	<-e.f.faceSem
	jcWaitStatus(t, e.f, id, statusCompleted)
}
