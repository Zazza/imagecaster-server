package desktop

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComponentState(t *testing.T) {
	tests := []struct {
		name       string
		installed  bool
		installing bool
		err        string
		want       string
	}{
		{"nothing", false, false, "", "wait"},
		{"installed", true, false, "", "ok"},
		{"installing", false, true, "", "run"},
		{"error", false, false, "boom", "fail"},
		{"installed beats installing and error", true, true, "boom", "ok"},
		{"installing beats error", false, true, "boom", "run"},
		{"installed beats error", true, false, "boom", "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ComponentState(tt.installed, tt.installing, tt.err))
		})
	}
}

func TestServiceState(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		healthy bool
		want    string
	}{
		{"healthy running", "running", true, "ready"},
		{"healthy external stopped", "stopped", true, "ready"},
		{"healthy empty status", "", true, "ready"},
		{"healthy crashed", "crashed", true, "ready"},
		{"running not healthy", "running", false, "starting"},
		{"starting", "starting", false, "starting"},
		{"crashed", "crashed", false, "failed"},
		{"unhealthy", "unhealthy", false, "failed"},
		{"stopped", "stopped", false, "off"},
		{"empty", "", false, "off"},
		{"unknown", "weird", false, "off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ServiceState(tt.status, tt.healthy))
		})
	}
}

func TestPhaseOf(t *testing.T) {
	ok := func(k string) Component { return Component{Key: k, Label: k, State: "ok"} }
	tests := []struct {
		name      string
		cs        []Component
		sd        string
		wantPhase string
		wantErr   string
	}{
		{
			name:      "first failed component reported",
			cs:        []Component{ok("a"), {Key: "neo", Label: "Forge Neo", State: "fail", Error: "disk full"}, {Key: "ol", Label: "Ollama", State: "fail", Error: "net"}},
			sd:        "ready",
			wantPhase: "failed",
			wantErr:   "Forge Neo: disk full",
		},
		{
			name:      "fail beats running",
			cs:        []Component{{Key: "a", Label: "A", State: "run"}, {Key: "b", Label: "B", State: "fail", Error: "x"}},
			sd:        "off",
			wantPhase: "failed",
			wantErr:   "B: x",
		},
		{
			name:      "running component",
			cs:        []Component{ok("a"), {Key: "b", Label: "B", State: "run"}},
			sd:        "ready",
			wantPhase: "installing",
		},
		{
			name:      "waiting component",
			cs:        []Component{{Key: "b", Label: "B", State: "wait"}},
			sd:        "failed",
			wantPhase: "installing",
		},
		{"all ok sd ready", []Component{ok("a"), ok("b")}, "ready", "ready", ""},
		{"all ok sd failed", []Component{ok("a")}, "failed", "failed", ""},
		{"all ok sd starting", []Component{ok("a")}, "starting", "starting", ""},
		{"all ok sd off", []Component{ok("a")}, "off", "starting", ""},
		{"no components sd ready", nil, "ready", "ready", ""},
		{"no components sd off", []Component{}, "off", "starting", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phase, errText := PhaseOf(tt.cs, tt.sd)
			assert.Equal(t, tt.wantPhase, phase)
			assert.Equal(t, tt.wantErr, errText)
		})
	}
}

func TestInstalled(t *testing.T) {
	tests := []struct {
		name string
		cs   []Component
		want bool
	}{
		{"nil", nil, true},
		{"empty", []Component{}, true},
		{"all ok", []Component{{State: "ok"}, {State: "ok"}}, true},
		{"one running", []Component{{State: "ok"}, {State: "run"}}, false},
		{"one waiting", []Component{{State: "wait"}}, false},
		{"one failed", []Component{{State: "ok"}, {State: "fail"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Installed(tt.cs))
		})
	}
}

func TestConnectAddr(t *testing.T) {
	tests := []struct {
		name string
		ips  []string
		port int
		want string
	}{
		{"first ip", []string{"192.168.1.5", "10.0.0.2"}, 8080, "http://192.168.1.5:8080"},
		{"single ip", []string{"10.0.0.2"}, 7000, "http://10.0.0.2:7000"},
		{"no ips", nil, 8080, "http://127.0.0.1:8080"},
		{"empty ips", []string{}, 9000, "http://127.0.0.1:9000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ConnectAddr(tt.ips, tt.port))
		})
	}
}
