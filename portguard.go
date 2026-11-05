package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"imagecaster-server/config"
)

type warner interface {
	Warn(msg string)
}

type portGuards struct {
	servers []*http.Server
}

func startPortGuards(guards []config.PortGuard, proxy http.Handler, w warner) *portGuards {
	return startHeldPortGuards(guards, nil, proxy, w)
}

func startHeldPortGuards(guards []config.PortGuard, held map[string]net.Listener, proxy http.Handler, w warner) *portGuards {
	pg := &portGuards{}
	for _, g := range guards {
		ln, ok := held[g.ListenAddr]
		delete(held, g.ListenAddr)
		var err error
		if !ok {
			ln, err = net.Listen("tcp", g.ListenAddr)
		}
		if err != nil {
			msg := "port guard " + g.Process + " " + g.ListenAddr + ": " + err.Error()
			log.Printf("[guard] %s", msg)
			if w != nil {
				w.Warn(msg)
			}
			continue
		}
		srv := &http.Server{
			Handler:           guardHandler(g.ProxyPath, proxy),
			ReadHeaderTimeout: 30 * time.Second,
		}
		pg.servers = append(pg.servers, srv)
		log.Printf("[guard] %s: %s → GPU queue → %s", g.Process, g.ListenAddr, g.ProxyPath)
		go func(name string) {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("[guard] %s stopped: %v", name, err)
			}
		}(g.Process)
	}
	for addr, ln := range held {
		if err := ln.Close(); err != nil {
			log.Printf("[guard] close unused %s: %v", addr, err)
		}
	}
	return pg
}

func applyPortGuard(cfg *config.Config) ([]config.PortGuard, []string) {
	guards, warns, held := holdPortGuards(cfg)
	for addr, ln := range held {
		if err := ln.Close(); err != nil {
			log.Printf("[guard] probe close %s: %v", addr, err)
		}
	}
	return guards, warns
}

func holdPortGuards(cfg *config.Config) ([]config.PortGuard, []string, map[string]net.Listener) {
	var warns []string
	held := map[string]net.Listener{}
	guards := cfg.ApplyPortGuardIf(func(g config.PortGuard) bool {
		ln, err := listenIfFree(g.ListenAddr)
		if err != nil {
			msg := "port guard " + g.Process + " " + g.ListenAddr + " busy, left unguarded: " + err.Error()
			log.Printf("[guard] %s", msg)
			warns = append(warns, msg)
			return false
		}
		held[g.ListenAddr] = ln
		return true
	})
	return guards, warns, held
}

func listenIfFree(addr string) (net.Listener, error) {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 300*time.Millisecond); err == nil {
			c.Close()
			return nil, errors.New("something already answers on 127.0.0.1:" + port)
		}
	}
	return net.Listen("tcp", addr)
}

func guardHandler(proxyPath string, next http.Handler) http.Handler {
	prefix := strings.TrimSuffix(proxyPath, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = prefix + r.URL.Path
		if r.URL.RawPath != "" {
			r2.URL.RawPath = prefix + r.URL.RawPath
		}
		next.ServeHTTP(w, r2)
	})
}

func (pg *portGuards) shutdown(ctx context.Context) {
	if pg == nil {
		return
	}
	for _, srv := range pg.servers {
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("[guard] shutdown: %v", err)
		}
	}
}
