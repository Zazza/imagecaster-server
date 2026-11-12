package llamacpp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRouter struct {
	mu       sync.Mutex
	status   map[string]string
	failed   map[string]bool
	chats    []map[string]any
	unloads  []string
	loads    []string
	stream   []string
	reply    string
	finish   string
	srv      *httptest.Server
	path     string
	chatHook func()
}

func newFakeRouter(t *testing.T) *fakeRouter {
	fr := &fakeRouter{status: map[string]string{}, failed: map[string]bool{}, reply: "a red fox", finish: "stop"}
	fr.srv = httptest.NewServer(http.HandlerFunc(fr.serve))
	t.Cleanup(fr.srv.Close)
	return fr
}

func (fr *fakeRouter) setIDs(ids map[string]string) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	for _, id := range ids {
		if _, ok := fr.status[id]; !ok {
			fr.status[id] = "unloaded"
		}
	}
}

func (fr *fakeRouter) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	fr.mu.Lock()
	defer fr.mu.Unlock()
	switch r.URL.Path {
	case "/models":
		var data []map[string]any
		for id, st := range fr.status {
			data = append(data, map[string]any{"id": id, "status": map[string]any{"value": st, "failed": fr.failed[id], "exit_code": 1}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	case "/models/load":
		id := req["model"].(string)
		fr.loads = append(fr.loads, id)
		if _, ok := fr.status[id]; !ok {
			http.NotFound(w, r)
			return
		}
		if fr.failed[id] {
			fr.status[id] = "unloaded"
		} else {
			for k, v := range fr.status {
				if v == "loaded" {
					fr.status[k] = "unloaded"
				}
			}
			fr.status[id] = "loaded"
		}
		w.Write([]byte(`{"success":true}`))
	case "/models/unload":
		id := req["model"].(string)
		if fr.status[id] != "loaded" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"code":400,"message":"model is not running"}}`))
			return
		}
		fr.unloads = append(fr.unloads, id)
		fr.status[id] = "unloaded"
		w.Write([]byte(`{"success":true}`))
	case "/v1/chat/completions", "/v1/completions":
		req["_path"] = r.URL.Path
		fr.chats = append(fr.chats, req)
		if fr.chatHook != nil {
			fr.mu.Unlock()
			fr.chatHook()
			fr.mu.Lock()
		}
		if s, _ := req["stream"].(bool); s {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, c := range fr.stream {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\n", fr.finish)
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3},\"timings\":{\"prompt_ms\":5,\"predicted_ms\":30}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		if r.URL.Path == "/v1/completions" {
			json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"text": fr.reply, "finish_reason": fr.finish}}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 3}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model":   req["model"],
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": fr.reply}, "finish_reason": fr.finish}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 3},
			"timings": map[string]any{"prompt_ms": 5.0, "predicted_ms": 30.0},
		})
	default:
		http.NotFound(w, r)
	}
}

func (fr *fakeRouter) lastChat(t *testing.T) map[string]any {
	t.Helper()
	fr.mu.Lock()
	defer fr.mu.Unlock()
	require.NotEmpty(t, fr.chats)
	return fr.chats[len(fr.chats)-1]
}

type facadeEnv struct {
	f     *Facade
	fr    *fakeRouter
	store *Store
	now   time.Time
	nowMu sync.Mutex
}

func (e *facadeEnv) advance(d time.Duration) {
	e.nowMu.Lock()
	e.now = e.now.Add(d)
	e.nowMu.Unlock()
}

func newFacadeEnv(t *testing.T) *facadeEnv {
	t.Helper()
	e := &facadeEnv{fr: newFakeRouter(t), store: fixtureStore(t), now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	e.f = New(Config{
		Store:       e.store,
		RouterURL:   e.fr.srv.URL,
		PresetPath:  filepath.Join(t.TempDir(), "models.ini"),
		ContextSize: 8192,
		KeepAlive:   5 * time.Minute,
		Reload:      func(context.Context) error { e.fr.setIDs(e.f.IDs()); return nil },
		Now: func() time.Time {
			e.nowMu.Lock()
			defer e.nowMu.Unlock()
			return e.now
		},
		Poll: time.Millisecond,
	})
	require.NoError(t, e.f.Refresh(context.Background()))
	return e
}

func (e *facadeEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.f.ServeHTTP(rec, req)
	return rec
}

func (e *facadeEnv) id(name string) string {
	return e.f.IDs()[name]
}

const (
	qwen = "huihui_ai/qwen2.5-abliterate:14b"
	vl   = "hf.co/GitMylo/nsfwcaption-qwen3-vl-8b-v3-gguf:Q4_K_M"
)

func TestTagsAsOllama(t *testing.T) {
	e := newFacadeEnv(t)
	rec := e.do(t, http.MethodGet, "/api/tags", "")
	require.Equal(t, 200, rec.Code)
	var out struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Size    int64  `json:"size"`
			Digest  string `json:"digest"`
			Details struct {
				Family string `json:"family"`
				Format string `json:"format"`
			} `json:"details"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	byName := map[string]int{}
	for i, m := range out.Models {
		byName[m.Name] = i
		assert.Equal(t, m.Name, m.Model)
		assert.Len(t, m.Digest, 64)
		assert.Equal(t, "gguf", m.Details.Format)
	}
	require.Contains(t, byName, qwen)
	require.Contains(t, byName, vl)
	require.Contains(t, byName, "qwen2.5-chat-ru:latest")
	assert.Equal(t, "qwen3vl", out.Models[byName[vl]].Details.Family)
}

func TestV1ChatMapsNameAndDropsOllamaFields(t *testing.T) {
	e := newFacadeEnv(t)
	body := `{"model":"` + qwen + `","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}],"temperature":0.7,"max_tokens":300,"stream":false,"keep_alive":"10m","options":{"num_ctx":4096,"num_gpu":-1}}`
	rec := e.do(t, http.MethodPost, "/v1/chat/completions", body)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "a red fox", resp.Choices[0].Message.Content)

	sent := e.fr.lastChat(t)
	assert.Equal(t, e.id(qwen), sent["model"])
	assert.NotContains(t, sent, "keep_alive")
	assert.NotContains(t, sent, "options")
	assert.Equal(t, 300.0, sent["max_tokens"])
	assert.Equal(t, 0.7, sent["temperature"])
	assert.Equal(t, []string{e.id(qwen)}, e.fr.loads)
}

func TestV1ModelsOK(t *testing.T) {
	e := newFacadeEnv(t)
	rec := e.do(t, http.MethodGet, "/v1/models", "")
	require.Equal(t, 200, rec.Code)
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	var ids []string
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	assert.Contains(t, ids, qwen)
}

func TestChatVisionImagesBecomeImageURL(t *testing.T) {
	e := newFacadeEnv(t)
	png := "iVBORw0KGgoAAAANSUhEUgAAAAE="
	body := `{"model":"` + vl + `","messages":[{"role":"system","content":"tagger"},{"role":"user","content":"Describe","images":["` + png + `"]}],"stream":false,"options":{"temperature":0.4,"num_predict":200,"num_ctx":8192}}`
	rec := e.do(t, http.MethodPost, "/api/chat", body)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var out struct {
		Model   string `json:"model"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		Done       bool   `json:"done"`
		DoneReason string `json:"done_reason"`
		EvalCount  int    `json:"eval_count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, vl, out.Model)
	assert.Equal(t, "assistant", out.Message.Role)
	assert.Equal(t, "a red fox", out.Message.Content)
	assert.True(t, out.Done)
	assert.Equal(t, "stop", out.DoneReason)
	assert.Equal(t, 3, out.EvalCount)

	sent := e.fr.lastChat(t)
	assert.Equal(t, e.id(vl), sent["model"])
	assert.Equal(t, 200.0, sent["max_tokens"])
	assert.Equal(t, 0.4, sent["temperature"])
	msgs := sent["messages"].([]any)
	require.Len(t, msgs, 2)
	user := msgs[1].(map[string]any)
	parts := user["content"].([]any)
	require.Len(t, parts, 2)
	assert.Equal(t, map[string]any{"type": "text", "text": "Describe"}, parts[0])
	img := parts[1].(map[string]any)
	assert.Equal(t, "image_url", img["type"])
	assert.Equal(t, "data:image/png;base64,"+png, img["image_url"].(map[string]any)["url"])
}

func TestChatInjectsModelSystemAndParams(t *testing.T) {
	e := newFacadeEnv(t)
	rec := e.do(t, http.MethodPost, "/api/chat", `{"model":"`+qwen+`","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	sent := e.fr.lastChat(t)
	msgs := sent["messages"].([]any)
	require.Len(t, msgs, 2)
	assert.Equal(t, map[string]any{"role": "system", "content": "You are Qwen."}, msgs[0])
	assert.Equal(t, 0.3, sent["temperature"])
	assert.Equal(t, []any{"<|im_end|>"}, sent["stop"])
}

func readNDJSON(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		out = append(out, m)
	}
	return out
}

func TestChatStreamsNDJSONByDefault(t *testing.T) {
	e := newFacadeEnv(t)
	e.fr.stream = []string{"a ", "red ", "fox"}
	rec := e.do(t, http.MethodPost, "/api/chat", `{"model":"`+qwen+`","messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	lines := readNDJSON(t, rec.Body.Bytes())
	require.Len(t, lines, 4)
	var text string
	for _, l := range lines[:3] {
		assert.Equal(t, false, l["done"])
		text += l["message"].(map[string]any)["content"].(string)
	}
	assert.Equal(t, "a red fox", text)
	last := lines[3]
	assert.Equal(t, true, last["done"])
	assert.Equal(t, "stop", last["done_reason"])
	assert.Equal(t, 11.0, last["prompt_eval_count"])
	assert.Equal(t, 3.0, last["eval_count"])
	assert.Equal(t, true, e.fr.lastChat(t)["stream"])
}

func TestGenerateStreamAndNonStream(t *testing.T) {
	e := newFacadeEnv(t)
	e.fr.stream = []string{"x", "y"}
	rec := e.do(t, http.MethodPost, "/api/generate", `{"model":"`+qwen+`","prompt":"hello","system":"be short"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	lines := readNDJSON(t, rec.Body.Bytes())
	require.Len(t, lines, 3)
	assert.Equal(t, "x", lines[0]["response"])
	assert.Equal(t, true, lines[2]["done"])
	msgs := e.fr.lastChat(t)["messages"].([]any)
	assert.Equal(t, map[string]any{"role": "system", "content": "be short"}, msgs[0])
	assert.Equal(t, map[string]any{"role": "user", "content": "hello"}, msgs[1])

	rec = e.do(t, http.MethodPost, "/api/generate", `{"model":"`+qwen+`","prompt":"hello","stream":false}`)
	require.Equal(t, 200, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "a red fox", out["response"])
	assert.Equal(t, true, out["done"])

	rec = e.do(t, http.MethodPost, "/api/generate", `{"model":"`+qwen+`","prompt":"hello","raw":true,"stream":false}`)
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, "/v1/completions", e.fr.lastChat(t)["_path"])
	assert.Equal(t, "hello", e.fr.lastChat(t)["prompt"])
}

func TestGenerateKeepAliveZeroUnloads(t *testing.T) {
	e := newFacadeEnv(t)
	rec := e.do(t, http.MethodPost, "/api/chat", `{"model":"`+qwen+`","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	require.Equal(t, 200, rec.Code)

	rec = e.do(t, http.MethodGet, "/api/ps", "")
	require.Equal(t, 200, rec.Code)
	var ps struct {
		Models []struct {
			Name      string `json:"name"`
			Size      int64  `json:"size"`
			SizeVRAM  int64  `json:"size_vram"`
			ExpiresAt string `json:"expires_at"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ps))
	require.Len(t, ps.Models, 1)
	assert.Equal(t, qwen, ps.Models[0].Name)
	assert.Positive(t, ps.Models[0].Size)
	exp, err := time.Parse(time.RFC3339Nano, ps.Models[0].ExpiresAt)
	require.NoError(t, err)
	assert.Equal(t, e.now.Add(5*time.Minute), exp)

	rec = e.do(t, http.MethodPost, "/api/generate", `{"model":"`+qwen+`","keep_alive":0}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, true, out["done"])
	assert.Equal(t, "unload", out["done_reason"])
	assert.Equal(t, []string{e.id(qwen)}, e.fr.unloads)

	rec = e.do(t, http.MethodGet, "/api/ps", "")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ps))
	assert.Empty(t, ps.Models)
	assert.Equal(t, `{"models":[]}`, strings.TrimSpace(rec.Body.String()))
}

func TestGenerateEmptyPromptLoads(t *testing.T) {
	e := newFacadeEnv(t)
	rec := e.do(t, http.MethodPost, "/api/generate", `{"model":"`+qwen+`"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "load", out["done_reason"])
	assert.Equal(t, []string{e.id(qwen)}, e.fr.loads)
	assert.Empty(t, e.fr.chats)
}

func TestUnsupportedAndUnknownModels(t *testing.T) {
	e := newFacadeEnv(t)
	rec := e.do(t, http.MethodPost, "/api/chat", `{"model":"qwen2.5vl:7b","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Ollama's own format")
	assert.Contains(t, rec.Body.String(), "qwen25vl")

	rec = e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"nope:1","messages":[{"role":"user","content":"hi"}]}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "not found")
	assert.Empty(t, e.fr.chats)
}

func TestLoadFailureIsClear(t *testing.T) {
	e := newFacadeEnv(t)
	e.fr.mu.Lock()
	e.fr.failed[e.id(qwen)] = true
	e.fr.mu.Unlock()
	rec := e.do(t, http.MethodPost, "/api/chat", `{"model":"`+qwen+`","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "could not load")
}

func TestShowVersionDelete(t *testing.T) {
	e := newFacadeEnv(t)
	rec := e.do(t, http.MethodGet, "/api/version", "")
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"version"`)

	rec = e.do(t, http.MethodPost, "/api/show", `{"model":"`+vl+`"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var show struct {
		Details struct {
			Family string `json:"family"`
		} `json:"details"`
		Capabilities []string       `json:"capabilities"`
		ModelInfo    map[string]any `json:"model_info"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &show))
	assert.Equal(t, "qwen3vl", show.Details.Family)
	assert.Contains(t, show.Capabilities, "vision")
	assert.Equal(t, "qwen3vl", show.ModelInfo["general.architecture"])

	rec = e.do(t, http.MethodPost, "/api/show", `{"name":"nope"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = e.do(t, http.MethodDelete, "/api/delete", `{"model":"qwen2.5-chat-ru"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	_, err := e.store.Resolve("qwen2.5-chat-ru")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.NotContains(t, e.f.IDs(), "qwen2.5-chat-ru:latest")

	rec = e.do(t, http.MethodDelete, "/api/delete", `{"model":"qwen2.5-chat-ru"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestKeepAliveExpiry(t *testing.T) {
	cases := []struct {
		name      string
		keepAlive string
		after     time.Duration
		unloaded  bool
	}{
		{"default 5m not yet", "", 4 * time.Minute, false},
		{"default 5m expired", "", 6 * time.Minute, true},
		{"request 10m not yet", `,"keep_alive":"10m"`, 6 * time.Minute, false},
		{"seconds number", `,"keep_alive":30`, time.Minute, true},
		{"negative forever", `,"keep_alive":-1`, 1000 * time.Hour, false},
		{"zero right after", `,"keep_alive":0`, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newFacadeEnv(t)
			rec := e.do(t, http.MethodPost, "/api/chat", `{"model":"`+qwen+`","messages":[{"role":"user","content":"hi"}],"stream":false`+c.keepAlive+`}`)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			e.advance(c.after)
			e.f.Sweep(context.Background())
			if c.unloaded {
				assert.Equal(t, []string{e.id(qwen)}, e.fr.unloads)
			} else {
				assert.Empty(t, e.fr.unloads)
			}
		})
	}
}

func TestUnloadIdleSkipsBusyModel(t *testing.T) {
	e := newFacadeEnv(t)
	inside := make(chan struct{})
	release := make(chan struct{})
	e.fr.chatHook = func() {
		close(inside)
		<-release
	}
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		done <- e.do(t, http.MethodPost, "/api/chat", `{"model":"`+qwen+`","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	}()
	<-inside
	require.NoError(t, e.f.UnloadIdle(context.Background()))
	assert.Empty(t, e.fr.unloads, "model with a request in flight stays loaded")
	close(release)
	require.Equal(t, 200, (<-done).Code)
	e.fr.chatHook = nil

	require.NoError(t, e.f.UnloadIdle(context.Background()))
	assert.Equal(t, []string{e.id(qwen)}, e.fr.unloads)
}

func TestEmbedNotImplemented(t *testing.T) {
	e := newFacadeEnv(t)
	rec := e.do(t, http.MethodPost, "/api/embed", `{"model":"`+qwen+`","input":"x"}`)
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Contains(t, rec.Body.String(), "error")
}

func TestKeepAliveZeroOnBusyModelUnloadsAfterRequest(t *testing.T) {
	e := newFacadeEnv(t)
	inside := make(chan struct{})
	release := make(chan struct{})
	e.fr.chatHook = func() {
		close(inside)
		<-release
	}
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		done <- e.do(t, http.MethodPost, "/api/chat", `{"model":"`+qwen+`","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	}()
	<-inside
	rec := e.do(t, http.MethodPost, "/api/generate", `{"model":"`+qwen+`","keep_alive":0}`)
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"unload"`)
	e.fr.mu.Lock()
	assert.Empty(t, e.fr.unloads, "running request is not cut")
	e.fr.mu.Unlock()
	close(release)
	require.Equal(t, 200, (<-done).Code)
	e.fr.mu.Lock()
	defer e.fr.mu.Unlock()
	assert.Equal(t, []string{e.id(qwen)}, e.fr.unloads)
}

func TestRequestsWaitWhileRouterRestarts(t *testing.T) {
	e := newFacadeEnv(t)
	inReload := make(chan struct{})
	finish := make(chan struct{})
	e.f.cfg.Reload = func(context.Context) error {
		close(inReload)
		<-finish
		e.fr.setIDs(e.f.IDs())
		return nil
	}
	go e.f.Refresh(context.Background())
	<-inReload
	done := make(chan int)
	go func() {
		done <- e.do(t, http.MethodPost, "/api/chat", `{"model":"`+qwen+`","messages":[{"role":"user","content":"hi"}],"stream":false}`).Code
	}()
	select {
	case <-done:
		t.Fatal("request must wait until llama-server is back")
	case <-time.After(50 * time.Millisecond):
	}
	e.fr.mu.Lock()
	assert.Empty(t, e.fr.chats)
	e.fr.mu.Unlock()
	close(finish)
	assert.Equal(t, 200, <-done)
}
