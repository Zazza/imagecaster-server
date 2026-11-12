package models

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"imagecaster-server/api"
	"imagecaster-server/installer"
)

func (m *ModelManager) handleDownloadStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	typ := r.URL.Query().Get("type")
	downloadURL := r.URL.Query().Get("url")
	filename := r.URL.Query().Get("filename")

	if typ == "" || downloadURL == "" || filename == "" {
		api.WriteError(w, "type, url and filename are required", http.StatusBadRequest)
		return
	}

	backend := m.config.GetActiveBackend()
	if backend == nil {
		api.WriteError(w, "no active backend", http.StatusBadRequest)
		return
	}

	var dir string
	switch typ {
	case "sd":
		dir = backend.ModelsDir
	case "lora":
		dir = backend.LoraDir
	case "vae":
		dir = backend.VaeDir
	case "text_encoder":
		dir = textEncoderDir(backend)
	default:
		api.WriteError(w, "invalid type: must be sd, lora, vae or text_encoder", http.StatusBadRequest)
		return
	}

	if dir == "" {
		api.WriteError(w, "directory not configured for type: "+typ, http.StatusBadRequest)
		return
	}

	filename, err := safeFilename(filename)
	if err != nil {
		api.WriteError(w, err.Error(), http.StatusBadRequest)
		return
	}

	want, err := parseSHA256(r.URL.Query().Get("sha256"))
	if err != nil {
		api.WriteError(w, err.Error(), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		api.WriteError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	rep := &sseReporter{w: w, flusher: flusher, name: filename}
	log.Printf("[download] %s -> %s", filename, dir)
	skipped, err := m.fetch(r.Context(), downloadURL, dir, filename, want, fetchEvents{progress: rep.progress, status: rep.status})
	if err != nil {
		log.Printf("[download] %s: %v", filename, err)
		fmt.Fprintf(w, "data: [ERROR] %s\n\n", err.Error())
		flusher.Flush()
		return
	}
	if skipped {
		log.Printf("[download] %s already present, skipped", filename)
		rep.status("already downloaded")
	} else {
		log.Printf("[download] complete: %s (%s)", filename, installer.FormatBytes(rep.done))
	}
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

type sseReporter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	name     string
	done     int64
	lastDone int64
	lastTime int64
	lastLog  int64
	started  bool
}

func (s *sseReporter) status(msg string) {
	fmt.Fprintf(s.w, "data: %s\n\n", msg)
	s.flusher.Flush()
}

func (s *sseReporter) progress(done, total int64) {
	s.done = done
	now := time.Now().UnixMilli()
	if s.started && done-s.lastDone < 1024*1024 && now-s.lastTime < 500 && done != total {
		return
	}
	s.started = true
	s.lastTime = now
	s.lastDone = done
	var percent float64
	if total > 0 {
		percent = float64(done) / float64(total) * 100
	}
	payload, err := json.Marshal(map[string]interface{}{
		"downloaded": done,
		"total":      total,
		"percent":    percent,
	})
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "data: %s\n\n", payload)
	s.flusher.Flush()
	if now-s.lastLog >= 10000 || done == total {
		s.lastLog = now
		pct := ""
		if total > 0 {
			pct = fmt.Sprintf(" (%.0f%%)", percent)
		}
		log.Printf("[download] %s: %s / %s%s", s.name, installer.FormatBytes(done), installer.FormatBytes(total), pct)
	}
}
