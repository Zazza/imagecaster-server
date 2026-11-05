package main

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"imagecaster-server/config"
	"imagecaster-server/process"
)

type proxyRoute struct {
	proxy *httputil.ReverseProxy
	gate  *Gate
}

type ProxyHandler struct {
	mu      sync.RWMutex
	routes  map[string]*proxyRoute
	manager *process.ProcessManager
}

func NewProxyHandler(pm *process.ProcessManager, cfg *config.Config, gate *Gate) *ProxyHandler {
	ph := &ProxyHandler{
		routes:  make(map[string]*proxyRoute),
		manager: pm,
	}

	for _, pc := range cfg.Processes {
		if pc.ProxyPath == "" || pc.TargetURL == "" {
			continue
		}
		target, err := url.Parse(pc.TargetURL)
		if err != nil {
			continue
		}
		ph.routes[pc.ProxyPath] = &proxyRoute{proxy: newReverseProxy(target, pc.ProxyPath), gate: gate}
	}

	return ph
}

func (ph *ProxyHandler) UpdateRoute(proxyPath, targetURL string) error {
	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("parse target URL: %w", err)
	}
	ph.mu.Lock()
	var gate *Gate
	if rt, ok := ph.routes[proxyPath]; ok {
		gate = rt.gate
	}
	ph.routes[proxyPath] = &proxyRoute{proxy: newReverseProxy(target, proxyPath), gate: gate}
	ph.mu.Unlock()
	return nil
}

func (ph *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ph.mu.RLock()
	var rt *proxyRoute
	var prefix string
	for p, candidate := range ph.routes {
		if strings.HasPrefix(r.URL.Path, p) {
			rt = candidate
			prefix = p
			break
		}
	}
	ph.mu.RUnlock()

	if rt == nil {
		http.NotFound(w, r)
		return
	}

	handler := http.StripPrefix(prefix, rt.proxy)
	if rt.gate != nil && rt.gate.Enabled() {
		handler = rt.gate.Middleware(handler)
	}
	handler.ServeHTTP(w, r)
}

func newReverseProxy(target *url.URL, proxyPath string) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	if proxyPath == "/api/llm/" {
		director := proxy.Director
		proxy.Director = func(r *http.Request) {
			director(r)
			r.Host = target.Host
		}
	}
	proxy.FlushInterval = -1
	proxy.Transport = &http.Transport{
		ResponseHeaderTimeout: 0, // SD can take very long
	}
	return proxy
}
