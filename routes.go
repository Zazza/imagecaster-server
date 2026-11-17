package main

import (
	"imagecaster-server/installer"
	"imagecaster-server/models"
	"net/http"
	"strings"
)

func setupMux(handlers *Handlers, inst *installer.Installer, mm *models.ModelManager, bm *BackendManager, proxy *ProxyHandler) *http.ServeMux {
	mux := http.NewServeMux()
	handlers.RegisterRoutes(mux)
	mm.RegisterRoutes(mux)
	mm.RegisterDeleteRoutes(mux)
	bm.RegisterRoutes(mux)
	inst.RegisterRoutes(mux)

	mux.Handle("/api/sd/", proxy)
	mux.Handle("/api/llm/", proxy)

	mux.HandleFunc("/", handlers.handleRoot)

	return mux
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/sd/") || strings.HasPrefix(r.URL.Path, "/api/train/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
