package llamacpp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var optionMap = map[string]string{
	"temperature":       "temperature",
	"top_p":             "top_p",
	"top_k":             "top_k",
	"min_p":             "min_p",
	"seed":              "seed",
	"stop":              "stop",
	"num_predict":       "max_tokens",
	"repeat_penalty":    "repeat_penalty",
	"repeat_last_n":     "repeat_last_n",
	"presence_penalty":  "presence_penalty",
	"frequency_penalty": "frequency_penalty",
	"typical_p":         "typical_p",
	"mirostat":          "mirostat",
	"mirostat_tau":      "mirostat_tau",
	"mirostat_eta":      "mirostat_eta",
}

func applyOptions(dst map[string]any, opts map[string]any, override bool) {
	for k, v := range opts {
		to, ok := optionMap[k]
		if !ok {
			continue
		}
		if _, exists := dst[to]; exists && !override {
			continue
		}
		dst[to] = v
	}
}

func imageMime(b64 string) string {
	switch {
	case strings.HasPrefix(b64, "/9j/"):
		return "image/jpeg"
	case strings.HasPrefix(b64, "UklGR"):
		return "image/webp"
	case strings.HasPrefix(b64, "R0lGOD"):
		return "image/gif"
	}
	return "image/png"
}

func imagePart(img string) map[string]any {
	url := img
	if !strings.HasPrefix(img, "data:") {
		url = "data:" + imageMime(img) + ";base64," + img
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

func ollamaMessage(m map[string]any) map[string]any {
	role, _ := m["role"].(string)
	text, _ := m["content"].(string)
	imgs, _ := m["images"].([]any)
	out := map[string]any{"role": role}
	if len(imgs) == 0 {
		out["content"] = text
		return out
	}
	parts := []any{map[string]any{"type": "text", "text": text}}
	for _, i := range imgs {
		if s, ok := i.(string); ok && s != "" {
			parts = append(parts, imagePart(s))
		}
	}
	out["content"] = parts
	return out
}

func hasSystem(msgs []any) bool {
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
			return true
		}
	}
	return false
}

func (f *Facade) withModelDefaults(m Model, req map[string]any) {
	if msgs, ok := req["messages"].([]any); ok && !hasSystem(msgs) {
		if sys := strings.TrimSpace(f.store.Text(m, MediaSystem)); sys != "" {
			req["messages"] = append([]any{map[string]any{"role": "system", "content": sys}}, msgs...)
		}
	}
	applyOptions(req, f.store.Params(m), false)
}

func formatToResponse(v any) any {
	switch t := v.(type) {
	case string:
		if t == "json" {
			return map[string]any{"type": "json_object"}
		}
	case map[string]any:
		return map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "schema", "schema": t}}
	}
	return nil
}

type ollamaCall struct {
	generate bool
	raw      bool
	stream   bool
	name     string
	req      map[string]any
}

func (f *Facade) buildOllama(body map[string]any, generate bool) (ollamaCall, bool, error) {
	c := ollamaCall{generate: generate, stream: true, req: map[string]any{}}
	if s, ok := body["stream"].(bool); ok {
		c.stream = s
	}
	if opts, ok := body["options"].(map[string]any); ok {
		applyOptions(c.req, opts, true)
	}
	if rf := formatToResponse(body["format"]); rf != nil {
		c.req["response_format"] = rf
	}
	empty := false
	if generate {
		prompt, _ := body["prompt"].(string)
		imgs, _ := body["images"].([]any)
		empty = prompt == "" && len(imgs) == 0
		if raw, _ := body["raw"].(bool); raw {
			c.raw = true
			c.req["prompt"] = prompt
		} else {
			var msgs []any
			if sys, _ := body["system"].(string); sys != "" {
				msgs = append(msgs, map[string]any{"role": "system", "content": sys})
			}
			msgs = append(msgs, ollamaMessage(map[string]any{"role": "user", "content": prompt, "images": body["images"]}))
			c.req["messages"] = msgs
		}
	} else {
		in, _ := body["messages"].([]any)
		empty = len(in) == 0
		msgs := make([]any, 0, len(in))
		for _, m := range in {
			if mm, ok := m.(map[string]any); ok {
				msgs = append(msgs, ollamaMessage(mm))
			}
		}
		c.req["messages"] = msgs
	}
	return c, empty, nil
}

func (f *Facade) handleOllama(w http.ResponseWriter, r *http.Request, generate bool) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	ka, err := f.keepAlive(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	call, empty, err := f.buildOllama(body, generate)
	if err != nil {
		writeErr(w, err)
		return
	}
	m, id, err := f.resolve(r.Context(), modelField(body))
	if err != nil {
		writeErr(w, err)
		return
	}
	call.name = m.Name.String()
	start := f.now()
	if empty {
		reason := "load"
		if ka == 0 {
			reason = "unload"
			f.mu.Lock()
			busy := f.active[id] > 0
			if busy {
				f.pending[id] = true
				f.expires[id] = f.now()
			}
			f.mu.Unlock()
			if !busy {
				f.unload(r.Context(), id)
			}
		} else {
			f.begin(id)
			err := f.ensureLoaded(r.Context(), m, id)
			f.end(r.Context(), id, ka)
			if err != nil {
				writeErr(w, err)
				return
			}
		}
		out := f.doneLine(call, start, reason)
		if generate {
			out["response"] = ""
		} else {
			out["message"] = map[string]any{"role": "assistant", "content": ""}
		}
		writeJSON(w, 200, out)
		return
	}
	if !call.raw {
		f.withModelDefaults(m, call.req)
	} else {
		applyOptions(call.req, f.store.Params(m), false)
	}
	call.req["model"] = id
	call.req["stream"] = call.stream
	if call.stream {
		call.req["stream_options"] = map[string]any{"include_usage": true}
	}
	f.begin(id)
	defer f.end(r.Context(), id, ka)
	loadStart := f.now()
	if err := f.ensureLoaded(r.Context(), m, id); err != nil {
		writeErr(w, err)
		return
	}
	loadDur := f.now().Sub(loadStart)
	path := "/v1/chat/completions"
	if call.raw {
		path = "/v1/completions"
	}
	resp, err := f.forward(r.Context(), path, call.req)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		writeJSON(w, resp.StatusCode, map[string]string{"error": routerError(b)})
		return
	}
	if call.stream {
		f.streamOllama(w, resp.Body, call, start, loadDur)
		return
	}
	var out struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage   usage   `json:"usage"`
		Timings timings `json:"timings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out); err != nil {
		writeErr(w, fmt.Errorf("llama-server response: %w", err))
		return
	}
	text, reason := "", "stop"
	if len(out.Choices) > 0 {
		text = out.Choices[0].Message.Content
		if call.raw {
			text = out.Choices[0].Text
		}
		reason = orStr(out.Choices[0].FinishReason, "stop")
	}
	line := f.doneLine(call, start, reason)
	f.stats(line, out.Usage, out.Timings, loadDur)
	f.setText(line, call, text)
	writeJSON(w, 200, line)
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type timings struct {
	PromptMS    float64 `json:"prompt_ms"`
	PredictedMS float64 `json:"predicted_ms"`
}

func (f *Facade) doneLine(c ollamaCall, start time.Time, reason string) map[string]any {
	return map[string]any{
		"model":          c.name,
		"created_at":     f.now().UTC().Format(time.RFC3339Nano),
		"done":           true,
		"done_reason":    reason,
		"total_duration": f.now().Sub(start).Nanoseconds(),
	}
}

func (f *Facade) stats(line map[string]any, u usage, t timings, load time.Duration) {
	line["load_duration"] = load.Nanoseconds()
	line["prompt_eval_count"] = u.PromptTokens
	line["prompt_eval_duration"] = int64(t.PromptMS * 1e6)
	line["eval_count"] = u.CompletionTokens
	line["eval_duration"] = int64(t.PredictedMS * 1e6)
}

func (f *Facade) setText(line map[string]any, c ollamaCall, text string) {
	if c.generate {
		line["response"] = text
		return
	}
	line["message"] = map[string]any{"role": "assistant", "content": text}
}

func (f *Facade) forward(ctx context.Context, path string, req map[string]any) (*http.Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, f.router.url(path), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := f.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("llama-server: %w", err)
	}
	return resp, nil
}

type sseChunk struct {
	Choices []struct {
		Text  string `json:"text"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage   *usage   `json:"usage"`
	Timings *timings `json:"timings"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (f *Facade) streamOllama(w http.ResponseWriter, body io.Reader, c ollamaCall, start time.Time, load time.Duration) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	emit := func(v any) {
		enc.Encode(v)
		if fl != nil {
			fl.Flush()
		}
	}
	reason := "stop"
	var u usage
	var t timings
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ch sseChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		if ch.Error != nil {
			emit(map[string]string{"error": ch.Error.Message})
			return
		}
		if ch.Usage != nil {
			u = *ch.Usage
		}
		if ch.Timings != nil {
			t = *ch.Timings
		}
		for _, choice := range ch.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				reason = *choice.FinishReason
			}
			text := choice.Delta.Content
			if c.raw {
				text = choice.Text
			}
			if text == "" {
				continue
			}
			line := map[string]any{"model": c.name, "created_at": f.now().UTC().Format(time.RFC3339Nano), "done": false}
			f.setText(line, c, text)
			emit(line)
		}
	}
	if err := sc.Err(); err != nil {
		emit(map[string]string{"error": "llama-server stream: " + err.Error()})
		return
	}
	line := f.doneLine(c, start, reason)
	f.stats(line, u, t, load)
	f.setText(line, c, "")
	emit(line)
}

func (f *Facade) handleV1(w http.ResponseWriter, r *http.Request, path string) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	ka, err := f.keepAlive(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	m, id, err := f.resolve(r.Context(), modelField(body))
	if err != nil {
		writeErr(w, err)
		return
	}
	opts, _ := body["options"].(map[string]any)
	delete(body, "options")
	delete(body, "keep_alive")
	applyOptions(body, opts, false)
	if path == "/v1/chat/completions" {
		f.withModelDefaults(m, body)
	} else {
		applyOptions(body, f.store.Params(m), false)
	}
	body["model"] = id
	f.begin(id)
	defer f.end(r.Context(), id, ka)
	if err := f.ensureLoaded(r.Context(), m, id); err != nil {
		writeJSON(w, errCode(err), map[string]any{"error": map[string]any{"message": err.Error(), "type": "invalid_request_error", "code": errCode(err)}})
		return
	}
	resp, err := f.forward(r.Context(), path, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}
