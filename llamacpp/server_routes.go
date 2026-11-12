package llamacpp

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	RouteServerLLM       = "/api/server/models/llm"
	RouteServerLLMPull   = "/api/server/models/llm/pull"
	RouteServerLLMDelete = "/api/server/models/delete/llm/"
)

func IsServerRoute(p string) bool {
	return p == RouteServerLLM || p == RouteServerLLMPull || strings.HasPrefix(p, RouteServerLLMDelete)
}

func (f *Facade) ServeServerModels(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == RouteServerLLMPull:
		f.serverPullStream(w, r)
	case strings.HasPrefix(r.URL.Path, RouteServerLLMDelete):
		f.serverDelete(w, r)
	case r.URL.Path == RouteServerLLM:
		f.serverModels(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *Facade) serverModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := f.store.List()
		if err != nil {
			writeErr(w, err)
			return
		}
		out := make([]map[string]string, 0, len(list))
		for _, m := range list {
			out = append(out, map[string]string{"name": m.Name.String(), "size": gb(m.Size)})
		}
		writeJSON(w, 200, out)
	case http.MethodPost:
		body, err := readBody(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		name := modelField(body)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		if err := f.Pull(r.Context(), name, nil); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "pulled", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *Facade) serverPullStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	send := func(s string) {
		fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(s, "\n", " "))
		if fl != nil {
			fl.Flush()
		}
	}
	if err := f.Pull(r.Context(), name, func(p Progress) { send(FormatProgress(p)) }); err != nil {
		send("[ERROR] " + err.Error())
		return
	}
	send("[DONE]")
}

func (f *Facade) serverDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), RouteServerLLMDelete))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad name"})
		return
	}
	if err := f.Delete(r.Context(), name); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted", "name": name})
}
