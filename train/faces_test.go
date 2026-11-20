package train

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/faceswap"
)

type fakeAnalyzer struct {
	fakeIdentity
	faces []faceswap.FaceInfo
	err   error
	refs  [][]float32
	size  image.Point
}

func (f *fakeAnalyzer) AnalyzeFaces(_ context.Context, img image.Image, refs [][]float32) ([]faceswap.FaceInfo, error) {
	f.refs = refs
	f.size = img.Bounds().Size()
	return f.faces, f.err
}

func facesPNG(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func postFaces(t *testing.T, m *Manager, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/train/faces", strings.NewReader(body)))
	return rec
}

func TestFaces_ReturnsFacesAndSize(t *testing.T) {
	e := newEnv(t)
	sim := 0.7
	a := &fakeAnalyzer{fakeIdentity: fakeIdentity{available: true}, faces: []faceswap.FaceInfo{{Box: [4]float64{0.1, 0.1, 0.3, 0.4}, HeightPx: 120, Yaw: 5, Sharpness: 80, Embedding: []float32{1, 0}, Similarity: &sim}}}
	e.m.SetIdentityBuilder(a)
	body, _ := json.Marshal(map[string]any{"image_base64": facesPNG(t, 40, 30), "refs": [][]float32{{1, 0}}})
	rec := postFaces(t, e.m, string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Faces  []faceswap.FaceInfo `json:"faces"`
		Width  int                 `json:"width"`
		Height int                 `json:"height"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, 40, got.Width)
	assert.Equal(t, 30, got.Height)
	require.Len(t, got.Faces, 1)
	assert.Equal(t, 120, got.Faces[0].HeightPx)
	require.NotNil(t, got.Faces[0].Similarity)
	assert.Equal(t, 0.7, *got.Faces[0].Similarity)
	assert.Equal(t, [][]float32{{1, 0}}, a.refs)
}

func TestFaces_Errors(t *testing.T) {
	tests := []struct {
		name     string
		builder  IdentityBuilder
		body     string
		wantCode int
		wantErr  string
	}{
		{"no analyzer", &fakeIdentity{available: true}, `{}`, http.StatusServiceUnavailable, "face_models_missing"},
		{"models missing", &fakeAnalyzer{}, `{}`, http.StatusServiceUnavailable, "face_models_missing"},
		{"bad json", &fakeAnalyzer{fakeIdentity: fakeIdentity{available: true}}, `{`, http.StatusBadRequest, "invalid_request"},
		{"bad image", &fakeAnalyzer{fakeIdentity: fakeIdentity{available: true}}, `{"image_base64":"bm90IGFuIGltYWdl"}`, http.StatusBadRequest, "invalid_image"},
		{"engine fails", &fakeAnalyzer{fakeIdentity: fakeIdentity{available: true}, err: errors.New("boom")}, "", http.StatusInternalServerError, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.m.SetIdentityBuilder(tt.builder)
			body := tt.body
			if body == "" {
				b, _ := json.Marshal(map[string]any{"image_base64": facesPNG(t, 8, 8)})
				body = string(b)
			}
			rec := postFaces(t, e.m, body)
			assert.Equal(t, tt.wantCode, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantErr)
		})
	}
}

func TestFaces_TooManyRefsAreCut(t *testing.T) {
	e := newEnv(t)
	a := &fakeAnalyzer{fakeIdentity: fakeIdentity{available: true}}
	e.m.SetIdentityBuilder(a)
	refs := make([][]float32, maxFaceRefs+20)
	for i := range refs {
		refs[i] = []float32{1}
	}
	body, _ := json.Marshal(map[string]any{"image_base64": facesPNG(t, 8, 8), "refs": refs})
	rec := postFaces(t, e.m, string(body))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, a.refs, maxFaceRefs)
}
