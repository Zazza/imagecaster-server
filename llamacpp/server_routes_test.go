package llamacpp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerModelRoutes(t *testing.T) {
	e := newFacadeEnv(t)
	reg := newFakeRegistry()
	reg.add("library/newm", "latest", "qwen2", fixtureLayer{MediaModel, []byte("GGUF-new")})
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	e.f.cfg.Puller = &Puller{Store: e.store, BaseURL: func(string) string { return srv.URL }}
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.f.ServeServerModels(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	rec := serve(http.MethodGet, RouteServerLLM, "")
	require.Equal(t, 200, rec.Code)
	var list []map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	names := map[string]string{}
	for _, m := range list {
		names[m["name"]] = m["size"]
	}
	assert.Equal(t, "119 B", names["qwen2.5-chat-ru:latest"], "model 7 B + config 112 B, as ollama list")

	rec = serve(http.MethodGet, RouteServerLLMPull+"?name=newm", "")
	require.Equal(t, 200, rec.Code)
	assert.True(t, strings.HasSuffix(strings.TrimSpace(rec.Body.String()), "data: [DONE]"), rec.Body.String())

	rec = serve(http.MethodGet, RouteServerLLMPull+"?name=missing", "")
	assert.Contains(t, rec.Body.String(), "data: [ERROR]")

	rec = serve(http.MethodDelete, RouteServerLLMDelete+"newm:latest", "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	_, err := e.store.Resolve("newm")
	assert.ErrorIs(t, err, ErrNotFound)

	rec = serve(http.MethodPost, RouteServerLLM, `{"name":"newm"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"pulled"`)
	assert.True(t, IsServerRoute(RouteServerLLMDelete+"x"))
	assert.False(t, IsServerRoute("/api/server/models/sd"))
}
