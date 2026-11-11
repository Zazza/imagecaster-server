//go:build dwpose_live

package dwpose

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type livePerson struct {
	Pose []float64 `json:"pose_keypoints_2d"`
	Face []float64 `json:"face_keypoints_2d"`
}

type liveResp struct {
	Poses []struct {
		People       []livePerson `json:"people"`
		CanvasWidth  int          `json:"canvas_width"`
		CanvasHeight int          `json:"canvas_height"`
	} `json:"poses"`
}

func worstDiff(got, want []float64, skipZeroWant bool) (worst float64, idx int) {
	idx = -1
	for i := 0; i+2 < len(want); i += 3 {
		if skipZeroWant && want[i+2] == 0 {
			continue
		}
		d := math.Max(math.Abs(got[i]-want[i]), math.Abs(got[i+1]-want[i+1]))
		if want[i+2] != 0 && got[i+2] == 0 {
			d = math.Inf(1)
		}
		if idx < 0 || d > worst {
			worst, idx = d, i/3
		}
	}
	return worst, idx
}

func TestLiveMatchesForge(t *testing.T) {
	lib, models, refDir := os.Getenv("DWPOSE_LIB"), os.Getenv("DWPOSE_MODELS"), os.Getenv("DWPOSE_REF_DIR")
	if lib == "" || models == "" || refDir == "" {
		t.Skip("set DWPOSE_LIB, DWPOSE_MODELS and DWPOSE_REF_DIR")
	}

	portrait, err := os.ReadFile(filepath.Join(refDir, "portrait.b64"))
	require.NoError(t, err)
	refRaw, err := os.ReadFile(filepath.Join(refDir, "forge_detect_portrait.json"))
	require.NoError(t, err)
	var ref liveResp
	require.NoError(t, json.Unmarshal(refRaw, &ref))
	require.Len(t, ref.Poses, 1)
	require.Len(t, ref.Poses[0].People, 1)
	want := ref.Poses[0].People[0]
	require.Len(t, want.Face, 210)
	require.Len(t, want.Pose, 54)

	est := NewONNX(Config{LibPath: lib, ModelsDir: models})
	defer est.Close()

	body, err := json.Marshal(map[string]any{
		"controlnet_module":        "dw_openpose_full",
		"controlnet_input_images":  []string{strings.TrimSpace(string(portrait))},
		"controlnet_processor_res": 1024,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/controlnet/detect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewHandler(est).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got liveResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
	require.Len(t, got.Poses, 1)
	pose := got.Poses[0]
	require.Equal(t, 1024, pose.CanvasWidth)
	require.Equal(t, 1536, pose.CanvasHeight)
	require.Len(t, pose.People, 1, "exactly one person expected")
	p := pose.People[0]
	require.Len(t, p.Face, 210, "70 face points expected")
	require.Len(t, p.Pose, 54, "18 body points expected")

	fw, fi := worstDiff(p.Face, want.Face, false)
	t.Logf("face: worst point %d off by %.3f px", fi, fw)
	require.LessOrEqualf(t, fw, 2.0, "face point %d: got (%.2f, %.2f), forge (%.2f, %.2f), off by %.3f px",
		fi, p.Face[3*fi], p.Face[3*fi+1], want.Face[3*fi], want.Face[3*fi+1], fw)

	bw, bi := worstDiff(p.Pose, want.Pose, true)
	t.Logf("body: worst point %d off by %.3f px", bi, bw)
	require.GreaterOrEqual(t, bi, 0, "reference has no visible body points")
	require.LessOrEqualf(t, bw, 4.0, "body point %d: got (%.2f, %.2f, %.0f), forge (%.2f, %.2f), off by %.3f px",
		bi, p.Pose[3*bi], p.Pose[3*bi+1], p.Pose[3*bi+2], want.Pose[3*bi], want.Pose[3*bi+1], bw)
}
