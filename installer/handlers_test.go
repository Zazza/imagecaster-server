package installer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

type logsResponse struct {
	Key   string   `json:"key"`
	Lines int      `json:"lines"`
	Logs  []string `json:"logs"`
}

func TestInstallHandlerInstallsInBackground(t *testing.T) {
	srv, hits := zipServer(t, nil)
	inst := newLockInstaller(t, logsConfig(t, srv.URL), srv.URL)
	mux := http.NewServeMux()
	inst.RegisterRoutes(mux)

	rec := serve(t, mux, http.MethodPost, "/api/server/install/testbackend")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, map[string]string{"status": "installing", "key": "testbackend"}, decode[map[string]string](t, rec))

	require.Eventually(t, func() bool { return inst.Status()["testbackend"].Installed }, 10*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, atomic.LoadInt32(hits))

	one := serve(t, mux, http.MethodGet, "/api/server/install/status/testbackend")
	require.Equal(t, http.StatusOK, one.Code)
	got := decode[InstallStatus](t, one)
	assert.True(t, got.Installed)
	assert.False(t, got.Installing)
	assert.Empty(t, got.Error)

	all := serve(t, mux, http.MethodGet, "/api/server/install/status")
	require.Equal(t, http.StatusOK, all.Code)
	assert.True(t, decode[map[string]InstallStatus](t, all)["testbackend"].Installed)

	full := inst.Logs("testbackend", 1000)
	require.GreaterOrEqual(t, len(full), 3)
	tests := []struct {
		name      string
		query     string
		wantLines int
		wantLogs  []string
	}{
		{name: "default", query: "", wantLines: 50, wantLogs: full},
		{name: "last two", query: "?lines=2", wantLines: 2, wantLogs: full[len(full)-2:]},
		{name: "zero ignored", query: "?lines=0", wantLines: 50, wantLogs: full},
		{name: "negative ignored", query: "?lines=-3", wantLines: 50, wantLogs: full},
		{name: "not a number ignored", query: "?lines=abc", wantLines: 50, wantLogs: full},
	}
	for _, tt := range tests {
		t.Run("logs "+tt.name, func(t *testing.T) {
			rec := serve(t, mux, http.MethodGet, "/api/server/install/logs/testbackend"+tt.query)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			got := decode[logsResponse](t, rec)
			assert.Equal(t, "testbackend", got.Key)
			assert.Equal(t, tt.wantLines, got.Lines)
			assert.Equal(t, tt.wantLogs, got.Logs)
		})
	}
}

func TestInstallHandlersRejectBadRequests(t *testing.T) {
	srv, hits := zipServer(t, nil)
	inst := newLockInstaller(t, logsConfig(t, srv.URL), srv.URL)
	mux := http.NewServeMux()
	inst.RegisterRoutes(mux)

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "install with GET", method: http.MethodGet, path: "/api/server/install/testbackend", want: http.StatusMethodNotAllowed},
		{name: "install without key", method: http.MethodPost, path: "/api/server/install/", want: http.StatusBadRequest},
		{name: "install nested key", method: http.MethodPost, path: "/api/server/install/a/b", want: http.StatusBadRequest},
		{name: "install unknown target", method: http.MethodPost, path: "/api/server/install/nope", want: http.StatusNotFound},
		{name: "status all with POST", method: http.MethodPost, path: "/api/server/install/status", want: http.StatusMethodNotAllowed},
		{name: "status one with POST", method: http.MethodPost, path: "/api/server/install/status/testbackend", want: http.StatusMethodNotAllowed},
		{name: "status one without key", method: http.MethodGet, path: "/api/server/install/status/", want: http.StatusBadRequest},
		{name: "status one unknown", method: http.MethodGet, path: "/api/server/install/status/nope", want: http.StatusNotFound},
		{name: "logs with POST", method: http.MethodPost, path: "/api/server/install/logs/testbackend", want: http.StatusMethodNotAllowed},
		{name: "logs without key", method: http.MethodGet, path: "/api/server/install/logs/", want: http.StatusBadRequest},
		{name: "logs unknown", method: http.MethodGet, path: "/api/server/install/logs/nope", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, mux, tt.method, tt.path)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}
	assert.EqualValues(t, 0, atomic.LoadInt32(hits))
	assert.False(t, inst.Status()["testbackend"].Installing)
}

func TestParsePositiveInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "1", want: 1},
		{in: "50", want: 50},
		{in: "007", want: 7},
		{in: "0", wantErr: true},
		{in: "000", wantErr: true},
		{in: "", wantErr: true},
		{in: "-5", wantErr: true},
		{in: "+5", wantErr: true},
		{in: "5x", wantErr: true},
		{in: "1.5", wantErr: true},
		{in: " 5", wantErr: true},
		{in: "٣", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(strconv.Quote(tt.in), func(t *testing.T) {
			t.Parallel()
			got, err := parsePositiveInt(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
