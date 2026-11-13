package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCORSNotForSDJobs(t *testing.T) {
	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, tc := range []struct {
		method, path string
		cors         bool
		code         int
	}{
		{http.MethodGet, "/api/sd/jobs/abc/result", false, http.StatusTeapot},
		{http.MethodOptions, "/api/sd/jobs", false, http.StatusTeapot},
		{http.MethodGet, "/api/server/status", true, http.StatusTeapot},
		{http.MethodOptions, "/api/server/status", true, http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		assert.Equal(t, tc.code, rec.Code, tc.path)
		assert.Equal(t, tc.cors, rec.Header().Get("Access-Control-Allow-Origin") == "*", tc.method+" "+tc.path)
	}
}
