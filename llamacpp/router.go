package llamacpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type RouterModel struct {
	ID     string `json:"id"`
	Status struct {
		Value    string `json:"value"`
		Failed   bool   `json:"failed"`
		ExitCode int    `json:"exit_code"`
	} `json:"status"`
}

func (m RouterModel) Loaded() bool {
	return m.Status.Value == "loaded" || m.Status.Value == "sleeping"
}

type Router struct {
	BaseURL string
	HTTP    *http.Client
	Poll    time.Duration
}

var ErrLoadFailed = errors.New("llama-server could not load the model")

func (r *Router) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return http.DefaultClient
}

func (r *Router) url(p string) string {
	return strings.TrimSuffix(r.BaseURL, "/") + p
}

func (r *Router) Models(ctx context.Context) ([]RouterModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url("/models"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("llama-server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llama-server /models: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []RouterModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("llama-server /models: %w", err)
	}
	return out.Data, nil
}

func (r *Router) post(ctx context.Context, path, id string) (int, string, error) {
	body, _ := json.Marshal(map[string]string{"model": id})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url(path), bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client().Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("llama-server: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, routerError(b), nil
}

func routerError(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(b))
}

func (r *Router) Unload(ctx context.Context, id string) error {
	code, msg, err := r.post(ctx, "/models/unload", id)
	if err != nil {
		return err
	}
	if code == http.StatusOK || (code == http.StatusBadRequest && strings.Contains(msg, "not running")) {
		return nil
	}
	return fmt.Errorf("llama-server unload %s: HTTP %d %s", id, code, msg)
}

func (r *Router) status(ctx context.Context, id string) (RouterModel, bool, error) {
	list, err := r.Models(ctx)
	if err != nil {
		return RouterModel{}, false, err
	}
	for _, m := range list {
		if m.ID == id {
			return m, true, nil
		}
	}
	return RouterModel{}, false, nil
}

func (r *Router) EnsureLoaded(ctx context.Context, id string) error {
	m, ok, err := r.status(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if m.Loaded() {
		return nil
	}
	if m.Status.Value != "loading" {
		code, msg, err := r.post(ctx, "/models/load", id)
		if err != nil {
			return err
		}
		if code != http.StatusOK && !strings.Contains(msg, "already") {
			return fmt.Errorf("llama-server load %s: HTTP %d %s", id, code, msg)
		}
	}
	poll := r.Poll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	seenLoading := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
		m, ok, err = r.status(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case !ok:
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		case m.Loaded():
			return nil
		case m.Status.Failed:
			return fmt.Errorf("%w (exit code %d)", ErrLoadFailed, m.Status.ExitCode)
		case m.Status.Value == "loading":
			seenLoading = true
		case seenLoading && m.Status.Value == "unloaded":
			return ErrLoadFailed
		}
	}
}

func (r *Router) Loaded(ctx context.Context) ([]string, error) {
	list, err := r.Models(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range list {
		if m.Loaded() || m.Status.Value == "loading" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}
