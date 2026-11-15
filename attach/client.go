package attach

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"imagecaster-server/gpu"
	"imagecaster-server/gpuqueue"
	"imagecaster-server/health"
	"imagecaster-server/i18n"
	"imagecaster-server/installer"
	"imagecaster-server/process"
	"imagecaster-server/textutil"
	"imagecaster-server/tui"
)

const (
	DefaultPort = 8080

	pollInterval = 3 * time.Second
	maxDelay     = 30 * time.Second

	preflightTimeout = 5 * time.Second
	pollTimeout      = 10 * time.Second
	controlTimeout   = 20 * time.Second
	logsTimeout      = 2 * time.Second

	bannerMaxErrRunes = 60

	maxBodyBytes = 2 << 20
)

type Target struct {
	Host string
	Port int
	Base string
}

func (t Target) HostPort() string {
	if strings.Contains(t.Host, ":") {
		return fmt.Sprintf("[%s]:%d", t.Host, t.Port)
	}
	return fmt.Sprintf("%s:%d", t.Host, t.Port)
}

func ParseTarget(arg string) (Target, error) {
	s := strings.TrimSpace(arg)
	if s == "" {
		return defaultTarget(), nil
	}
	if strings.HasPrefix(s, "-") {
		return Target{}, fmt.Errorf("unexpected argument %q (usage: attach [host[:port]])", arg)
	}
	s = strings.TrimPrefix(s, "http://")
	if strings.Contains(s, "://") {
		return Target{}, fmt.Errorf("unsupported target %q", arg)
	}
	s = strings.TrimSuffix(s, "/")

	host := s
	port := DefaultPort
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return Target{}, fmt.Errorf("missing closing bracket in %q", arg)
		}
		host = s[1:end]
		rest := s[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return Target{}, fmt.Errorf("invalid port in %q", arg)
			}
			n, err := strconv.Atoi(rest[1:])
			if err != nil {
				return Target{}, fmt.Errorf("invalid port in %q", arg)
			}
			port = n
		}
	} else if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Target{}, fmt.Errorf("invalid port in %q", arg)
		}
		host = h
		port = n
	}

	if host == "" {
		return Target{}, fmt.Errorf("empty host in %q", arg)
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return Target{}, fmt.Errorf("invalid IPv6 address %q", host)
	}
	if port < 1 || port > 65535 {
		return Target{}, fmt.Errorf("port %d out of range", port)
	}

	t := Target{Host: host, Port: port}
	t.Base = "http://" + t.HostPort()
	return t, nil
}

func defaultTarget() Target {
	t := Target{Host: "127.0.0.1", Port: DefaultPort}
	t.Base = "http://" + t.HostPort()
	return t
}

type State int

const (
	StateConnecting State = iota
	StateOK
	StateLost
)

type sysJSON struct {
	CPUPercent float64 `json:"cpu_percent"`
	RAMUsage   float64 `json:"ram_usage"`
	RAMUsed    uint64  `json:"ram_used"`
	RAMTotal   uint64  `json:"ram_total"`
}

type statusResponse struct {
	Processes map[string]process.ProcessStatus   `json:"processes"`
	Health    map[string]health.HealthResult     `json:"health"`
	GPU       gpu.GPUInfo                        `json:"gpu"`
	Installs  map[string]installer.InstallStatus `json:"installs"`
	Sys       *sysJSON                           `json:"sys"`
}

type logsResponse struct {
	Logs []string `json:"logs"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type snapshot struct {
	processes map[string]process.ProcessStatus
	health    map[string]health.HealthResult
	gpu       gpu.GPUInfo
	installs  map[string]installer.InstallStatus
	sys       *sysJSON
	queue     gpuqueue.Status
	logCache  map[string][]string
}

type Client struct {
	target    Target
	poll      *http.Client
	ctrl      *http.Client
	logs      *http.Client
	preflight *http.Client

	mu      sync.RWMutex
	snap    snapshot
	state   State
	lastErr string
	fails   int

	send func(tea.Msg)
}

func NewClient(t Target) *Client {
	return &Client{
		target:    t,
		poll:      &http.Client{Timeout: pollTimeout},
		ctrl:      &http.Client{Timeout: controlTimeout},
		logs:      &http.Client{Timeout: logsTimeout},
		preflight: &http.Client{Timeout: preflightTimeout},
	}
}

func (c *Client) Notify(send func(tea.Msg)) {
	c.mu.Lock()
	c.send = send
	c.mu.Unlock()
}

func (c *Client) Preflight() error {
	var sr statusResponse
	return c.getJSON(context.Background(), c.preflight, c.target.Base+"/api/server/status", &sr)
}

func (c *Client) Run(ctx context.Context) {
	for {
		if err := c.refreshOnce(ctx); err != nil {
			c.markFailed(err)
		} else {
			c.markOK()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(nextDelay(c.failsNow())):
		}
	}
}

func nextDelay(fails int) time.Duration {
	if fails <= 0 {
		return pollInterval
	}
	shift := fails
	if shift > 4 {
		shift = 4
	}
	d := pollInterval * time.Duration(1<<uint(shift))
	if d > maxDelay {
		return maxDelay
	}
	return d
}

func (c *Client) refreshOnce(ctx context.Context) error {
	var sr statusResponse
	if err := c.getJSON(ctx, c.poll, c.target.Base+"/api/server/status", &sr); err != nil {
		return fmt.Errorf("status: %w", err)
	}

	var qs gpuqueue.Status
	queueErr := c.getJSON(ctx, c.poll, c.target.Base+"/api/gpu/status", &qs)

	c.mu.Lock()
	c.snap.processes = sr.Processes
	c.snap.health = sr.Health
	c.snap.gpu = sr.GPU
	c.snap.installs = sr.Installs
	c.snap.sys = sr.Sys
	if queueErr == nil {
		c.snap.queue = qs
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) refreshAndMark(ctx context.Context) {
	if err := c.refreshOnce(ctx); err != nil {
		c.markFailed(err)
		return
	}
	c.markOK()
}

func (c *Client) markFailed(err error) {
	c.mu.Lock()
	changed := c.state != StateLost
	c.state = StateLost
	c.lastErr = err.Error()
	c.fails++
	c.mu.Unlock()
	if changed {
		c.notifyChange()
	}
}

func (c *Client) markOK() {
	c.mu.Lock()
	changed := c.state != StateOK
	c.state = StateOK
	c.lastErr = ""
	c.fails = 0
	c.mu.Unlock()
	if changed {
		c.notifyChange()
	}
}

func (c *Client) notifyChange() {
	c.mu.RLock()
	send := c.send
	c.mu.RUnlock()
	if send != nil {
		send(tui.ServicesChangeMsg{})
	}
}

func (c *Client) getJSON(ctx context.Context, hc *http.Client, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, endpoint)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

func (c *Client) doControl(method, endpoint, label string, body []byte) error {
	ctx := context.Background()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.ctrl.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var er errorResponse
		if derr := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&er); derr == nil && er.Error != "" {
			return fmt.Errorf("%s: %s", label, sanitizeString(er.Error))
		}
		return fmt.Errorf("%s: HTTP %d", label, resp.StatusCode)
	}
	c.refreshAndMark(ctx)
	c.notifyChange()
	return nil
}

func (c *Client) control(action, name string) error {
	endpoint := c.target.Base + "/api/server/" + action + "/" + url.PathEscape(name)
	return c.doControl(http.MethodPost, endpoint, action+" "+name, nil)
}

func (c *Client) queueMove(id string, dir int) error {
	move := "down"
	if dir < 0 {
		move = "up"
	}
	body, err := json.Marshal(map[string]string{"move": move})
	if err != nil {
		return fmt.Errorf("queue move %s: %w", id, err)
	}
	endpoint := c.target.Base + "/api/gpu/queue/" + url.PathEscape(id)
	return c.doControl(http.MethodPatch, endpoint, "queue move "+id, body)
}

func (c *Client) queueCancel(id string) error {
	endpoint := c.target.Base + "/api/gpu/lease/" + url.PathEscape(id)
	return c.doControl(http.MethodDelete, endpoint, "queue cancel "+id, nil)
}

func (c *Client) ProcLogs(name string, lines int) []string {
	if c.stateNow() == StateOK {
		if fetched, ok := c.fetchLogs(name, lines); ok {
			c.storeLogs(name, fetched)
			return fetched
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if cached, ok := c.snap.logCache[name]; ok {
		return cached
	}
	return nil
}

func (c *Client) fetchLogs(name string, lines int) ([]string, bool) {
	endpoint := fmt.Sprintf("%s/api/server/logs/%s?lines=%d", c.target.Base, url.PathEscape(name), lines)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false
	}
	resp, err := c.logs.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, false
	}
	var lr logsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&lr); err != nil {
		return nil, false
	}
	sanitized := make([]string, len(lr.Logs))
	for i, line := range lr.Logs {
		sanitized[i] = sanitizeString(line)
	}
	return sanitized, true
}

func (c *Client) storeLogs(name string, logs []string) {
	c.mu.Lock()
	if c.snap.logCache == nil {
		c.snap.logCache = make(map[string][]string)
	}
	c.snap.logCache[name] = logs
	c.mu.Unlock()
}

func (c *Client) stateNow() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Client) failsNow() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fails
}

func (c *Client) banner() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch c.state {
	case StateOK:
		return ""
	case StateConnecting:
		return fmt.Sprintf(i18n.T("attach.connecting"), c.target.Base)
	default:
		return fmt.Sprintf(i18n.T("attach.reconnecting"), c.fails, textutil.Ellipsis(sanitizeString(c.lastErr), bannerMaxErrRunes))
	}
}

func sanitizeString(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

func (c *Client) Deps() tui.ServerDeps {
	return tui.ServerDeps{
		Port: c.target.Port,
		ProcStatus: func() map[string]tui.ServiceInfo {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return tui.Services(c.snap.processes, nil, sanitizeString)
		},
		HealthResults: func() map[string]tui.HealthResult {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return tui.Health(c.snap.health, sanitizeString)
		},
		GPUInfo: func() tui.GPUInfo {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return tui.GPU(c.snap.gpu, sanitizeString)
		},
		InstallStatus: func() map[string]tui.ComponentInstallStatus {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return tui.Installs(c.snap.installs, sanitizeString)
		},
		QueueSnapshot: func() tui.QueueSnapshot {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return tui.Queue(c.snap.queue, sanitizeString)
		},
		QueueMove:   c.queueMove,
		QueueCancel: c.queueCancel,
		PollStats: func() tui.SysStats {
			c.mu.RLock()
			defer c.mu.RUnlock()
			if c.snap.sys == nil {
				return tui.SysStats{}
			}
			return tui.SysStats{
				CPUUsage: c.snap.sys.CPUPercent,
				RAMUsage: c.snap.sys.RAMUsage,
				RAMUsed:  c.snap.sys.RAMUsed,
				RAMTotal: c.snap.sys.RAMTotal,
			}
		},
		StartProc:   func(name string) error { return c.control("start", name) },
		StopProc:    func(name string) error { return c.control("stop", name) },
		RestartProc: func(name string) error { return c.control("restart", name) },
		ProcLogs:    c.ProcLogs,
		ConnState:   c.banner,
	}
}
