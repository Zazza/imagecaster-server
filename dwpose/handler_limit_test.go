package dwpose

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHandlerBodyLimit(t *testing.T) {
	body := detectBody(t, "dw_openpose_full", 512, grayB64(t, 64, 64))
	cases := []struct {
		name  string
		limit int64
		want  int
		calls int
	}{
		{name: "within limit", limit: int64(len(body)) + 10, want: http.StatusOK, calls: 1},
		{name: "over limit", limit: int64(len(body)) / 2, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			est := &fakeEstimator{}
			rec := serve(newHandler(est, tc.limit), "/controlnet/detect", body)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
			assert.Equal(t, tc.calls, est.calls())
		})
	}
	assert.EqualValues(t, 512<<20, maxBody)
}
