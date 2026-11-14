package desktop

import (
	"fmt"
	"net"
)

const (
	StateWait = "wait"
	StateRun  = "run"
	StateOK   = "ok"
	StateFail = "fail"
)

const (
	ServiceOff      = "off"
	ServiceStarting = "starting"
	ServiceReady    = "ready"
	ServiceFailed   = "failed"
)

const (
	PhaseInstalling = "installing"
	PhaseStarting   = "starting"
	PhaseReady      = "ready"
	PhaseFailed     = "failed"
)

type Component struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	State    string `json:"state"`
	Progress string `json:"progress"`
	Error    string `json:"error"`
}

type Snapshot struct {
	Components []Component `json:"components"`
	SD         string      `json:"sd"`
	LLM        string      `json:"llm"`
	Lines      []string    `json:"lines"`
	Phase      string      `json:"phase"`
	Error      string      `json:"error"`
	Addr       string      `json:"addr"`
	GPUJobs    int         `json:"gpu_jobs"`
}

func ComponentState(installed, installing bool, errText string) string {
	switch {
	case installed:
		return StateOK
	case installing:
		return StateRun
	case errText != "":
		return StateFail
	}
	return StateWait
}

func ServiceState(status string, healthy bool) string {
	if healthy {
		return ServiceReady
	}
	switch status {
	case "running", "starting":
		return ServiceStarting
	case "crashed", "unhealthy":
		return ServiceFailed
	}
	return ServiceOff
}

func PhaseOf(cs []Component, sd string) (string, string) {
	pending := false
	for _, c := range cs {
		switch c.State {
		case StateFail:
			return PhaseFailed, c.Label + ": " + c.Error
		case StateOK:
		default:
			pending = true
		}
	}
	if pending {
		return PhaseInstalling, ""
	}
	switch sd {
	case ServiceReady:
		return PhaseReady, ""
	case ServiceFailed:
		return PhaseFailed, ""
	}
	return PhaseStarting, ""
}

func Installed(cs []Component) bool {
	for _, c := range cs {
		if c.State != StateOK {
			return false
		}
	}
	return true
}

func ConnectAddr(ips []string, port int) string {
	host := "127.0.0.1"
	if len(ips) > 0 {
		host = ips[0]
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, fmt.Sprint(port)))
}

func LANIPs() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}
