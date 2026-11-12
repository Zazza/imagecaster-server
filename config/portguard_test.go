package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func guardCfg(procs map[string]ProcessConfig) *Config {
	return &Config{GPU: GPUConfig{GuardPorts: true}, Processes: procs}
}

func TestApplyPortGuard_Disabled(t *testing.T) {
	t.Parallel()
	c := NewDefault()
	c.GPU.GuardPorts = false
	before := NewDefault().Processes

	assert.Nil(t, c.ApplyPortGuard())
	assert.Equal(t, before, c.Processes)
}

func TestApplyPortGuard_Default(t *testing.T) {
	t.Parallel()
	c := ollamaDefault()
	guards := c.ApplyPortGuard()

	assert.Equal(t, []PortGuard{
		{Process: "ollama", ListenAddr: ":11434", ProxyPath: "/api/llm/"},
		{Process: "sd", ListenAddr: ":7860", ProxyPath: "/api/sd/"},
	}, guards)

	assert.Equal(t, "http://127.0.0.1:7861", c.Processes["sd"].TargetURL)
	assert.Equal(t, "http://127.0.0.1:7861/sdapi/v1/options", c.Processes["sd"].HealthURL)
	assert.Equal(t, "http://127.0.0.1:11435", c.Processes["ollama"].TargetURL)
	assert.Equal(t, "http://127.0.0.1:11435/api/tags", c.Processes["ollama"].HealthURL)
	assert.Equal(t, ollamaDefault().Processes["python"], c.Processes["python"])
}

func TestApplyPortGuard_LoopbackHosts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		listen string
	}{
		{"localhost", "http://localhost:7860", ":7860"},
		{"ipv4 loopback", "http://127.0.0.1:7860", ":7860"},
		{"ipv6 loopback", "http://[::1]:7860", ":7860"},
		{"other port", "http://localhost:9000", ":9000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := guardCfg(map[string]ProcessConfig{
				"sd": {AutoStart: true, TargetURL: tt.target, ProxyPath: "/api/sd/", InternalPort: 7861},
			})
			guards := c.ApplyPortGuard()
			require.Len(t, guards, 1)
			assert.Equal(t, PortGuard{Process: "sd", ListenAddr: tt.listen, ProxyPath: "/api/sd/"}, guards[0])
			assert.Equal(t, "http://127.0.0.1:7861", c.Processes["sd"].TargetURL)
		})
	}
}

func TestApplyPortGuard_SortedByKey(t *testing.T) {
	t.Parallel()
	c := guardCfg(map[string]ProcessConfig{
		"zeta":  {AutoStart: true, TargetURL: "http://localhost:9300", ProxyPath: "/api/z/", InternalPort: 9301},
		"alpha": {AutoStart: true, TargetURL: "http://localhost:9100", ProxyPath: "/api/a/", InternalPort: 9101},
		"mid":   {AutoStart: true, TargetURL: "http://localhost:9200", ProxyPath: "/api/m/", InternalPort: 9201},
	})
	guards := c.ApplyPortGuard()
	require.Len(t, guards, 3)
	assert.Equal(t, "alpha", guards[0].Process)
	assert.Equal(t, "mid", guards[1].Process)
	assert.Equal(t, "zeta", guards[2].Process)
	assert.Equal(t, ":9100", guards[0].ListenAddr)
	assert.Equal(t, ":9200", guards[1].ListenAddr)
	assert.Equal(t, ":9300", guards[2].ListenAddr)
}

func TestApplyPortGuard_HealthURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		health string
		want   string
	}{
		{"same loopback port moves", "http://localhost:7860/sdapi/v1/options", "http://127.0.0.1:7861/sdapi/v1/options"},
		{"same port via 127.0.0.1 moves", "http://127.0.0.1:7860/health", "http://127.0.0.1:7861/health"},
		{"other port kept", "http://localhost:9999/health", "http://localhost:9999/health"},
		{"other host kept", "http://192.168.1.50:7860/health", "http://192.168.1.50:7860/health"},
		{"empty kept", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := guardCfg(map[string]ProcessConfig{
				"sd": {AutoStart: true, TargetURL: "http://localhost:7860", HealthURL: tt.health, ProxyPath: "/api/sd/", InternalPort: 7861},
			})
			require.Len(t, c.ApplyPortGuard(), 1)
			assert.Equal(t, tt.want, c.Processes["sd"].HealthURL)
		})
	}
}

func TestApplyPortGuard_InternalPort(t *testing.T) {
	t.Parallel()
	t.Run("zero means public plus one", func(t *testing.T) {
		t.Parallel()
		c := guardCfg(map[string]ProcessConfig{
			"sd": {AutoStart: true, TargetURL: "http://localhost:7860", HealthURL: "http://localhost:7860/x", ProxyPath: "/api/sd/"},
		})
		guards := c.ApplyPortGuard()
		require.Len(t, guards, 1)
		assert.Equal(t, ":7860", guards[0].ListenAddr)
		assert.Equal(t, "http://127.0.0.1:7861", c.Processes["sd"].TargetURL)
		assert.Equal(t, "http://127.0.0.1:7861/x", c.Processes["sd"].HealthURL)
	})
	t.Run("equal to public not guarded", func(t *testing.T) {
		t.Parallel()
		pc := ProcessConfig{
			AutoStart:    true,
			TargetURL:    "http://localhost:7860",
			HealthURL:    "http://localhost:7860/x",
			ProxyPath:    "/api/sd/",
			InternalPort: 7860,
			Args:         []string{"launch.py", "--listen", "--port", "7860"},
		}
		c := guardCfg(map[string]ProcessConfig{"sd": pc})
		assert.Empty(t, c.ApplyPortGuard())
		assert.Equal(t, pc, c.Processes["sd"])
	})
}

func TestApplyPortGuard_NotGuarded(t *testing.T) {
	t.Parallel()
	remote := ProcessConfig{
		AutoStart:    true,
		TargetURL:    "http://192.168.1.50:7860",
		HealthURL:    "http://192.168.1.50:7860/sdapi/v1/options",
		ProxyPath:    "/api/sd/",
		InternalPort: 7861,
		Args:         []string{"launch.py", "--listen", "--port", "7860"},
	}
	remoteLLM := ProcessConfig{
		AutoStart:    true,
		TargetURL:    "http://10.0.0.5:11434",
		ProxyPath:    "/api/llm/",
		InternalPort: 11435,
		Env:          map[string]string{"OLLAMA_HOST": "0.0.0.0:11434"},
	}
	python := ProcessConfig{AutoStart: true, Name: "Python 3.10", Category: "utility"}
	noProxy := ProcessConfig{AutoStart: true, TargetURL: "http://localhost:5000", InternalPort: 5001}
	noTarget := ProcessConfig{AutoStart: true, ProxyPath: "/api/x/", InternalPort: 5001}

	c := guardCfg(map[string]ProcessConfig{
		"sd":       remote,
		"ollama":   remoteLLM,
		"python":   python,
		"noproxy":  noProxy,
		"notarget": noTarget,
	})
	assert.Empty(t, c.ApplyPortGuard())
	assert.Equal(t, remote, c.Processes["sd"])
	assert.Equal(t, "http://10.0.0.5:11434", c.Processes["ollama"].TargetURL)
	assert.Equal(t, "0.0.0.0:11434", c.Processes["ollama"].Env["OLLAMA_HOST"])
	assert.Equal(t, python, c.Processes["python"])
	assert.Equal(t, noProxy, c.Processes["noproxy"])
	assert.Equal(t, noTarget, c.Processes["notarget"])
}

func TestApplyPortGuard_OllamaEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pc   ProcessConfig
		want map[string]string
	}{
		{
			"by proxy path",
			ProcessConfig{
				AutoStart: true, TargetURL: "http://localhost:11434", ProxyPath: "/api/llm/", InternalPort: 11435,
				Env: map[string]string{"OLLAMA_HOST": "0.0.0.0:11434", "OLLAMA_MODELS": "m"},
			},
			map[string]string{"OLLAMA_HOST": "127.0.0.1:11435", "OLLAMA_MODELS": "m"},
		},
		{
			"by proxy path without env",
			ProcessConfig{AutoStart: true, TargetURL: "http://localhost:11434", ProxyPath: "/api/llm/", InternalPort: 11435},
			map[string]string{"OLLAMA_HOST": "127.0.0.1:11435"},
		},
		{
			"by OLLAMA_HOST env on other proxy path",
			ProcessConfig{
				AutoStart: true, TargetURL: "http://localhost:11434", ProxyPath: "/api/ollama2/", InternalPort: 11440,
				Env: map[string]string{"OLLAMA_HOST": "0.0.0.0:11434", "X": "y"},
			},
			map[string]string{"OLLAMA_HOST": "127.0.0.1:11440", "X": "y"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := guardCfg(map[string]ProcessConfig{"llm": tt.pc})
			require.Len(t, c.ApplyPortGuard(), 1)
			assert.Equal(t, tt.want, c.Processes["llm"].Env)
		})
	}
}

func TestApplyPortGuard_OllamaEnvNotAliased(t *testing.T) {
	t.Parallel()
	shared := map[string]string{"OLLAMA_HOST": "0.0.0.0:11434", "OLLAMA_MODELS": "m"}
	c := guardCfg(map[string]ProcessConfig{
		"ollama": {AutoStart: true, TargetURL: "http://localhost:11434", ProxyPath: "/api/llm/", InternalPort: 11435, Env: shared},
	})
	require.Len(t, c.ApplyPortGuard(), 1)
	assert.Equal(t, "127.0.0.1:11435", c.Processes["ollama"].Env["OLLAMA_HOST"])
	assert.Equal(t, "0.0.0.0:11434", shared["OLLAMA_HOST"])

	d := ollamaDefault()
	d.ApplyPortGuard()
	assert.Equal(t, "127.0.0.1:11435", d.Processes["ollama"].Env["OLLAMA_HOST"])
	assert.Equal(t, "0.0.0.0:11434", ollamaDefault().Processes["ollama"].Env["OLLAMA_HOST"])
}

func TestApplyPortGuard_SDArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			"card example",
			[]string{"launch.py", "--listen", "--api", "--xformers", "--port", "7860"},
			[]string{"launch.py", "--api", "--xformers", "--port", "7861"},
		},
		{
			"port equals form",
			[]string{"launch.py", "--port=7860", "--api"},
			[]string{"launch.py", "--api", "--port", "7861"},
		},
		{
			"server name both forms",
			[]string{"launch.py", "--server-name", "0.0.0.0", "--api", "--server-name=1.2.3.4", "--medvram-sdxl"},
			[]string{"launch.py", "--api", "--medvram-sdxl", "--port", "7861"},
		},
		{
			"multiple ports and listen",
			[]string{"--listen", "launch.py", "--port", "1", "--xformers", "--port=2", "--listen"},
			[]string{"launch.py", "--xformers", "--port", "7861"},
		},
		{
			"no port args",
			[]string{"launch.py", "--api"},
			[]string{"launch.py", "--api", "--port", "7861"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := guardCfg(map[string]ProcessConfig{
				"sd": {AutoStart: true, TargetURL: "http://localhost:7860", ProxyPath: "/api/sd/", InternalPort: 7861, Args: tt.args},
			})
			require.Len(t, c.ApplyPortGuard(), 1)
			assert.Equal(t, tt.want, c.Processes["sd"].Args)
		})
	}
}

func TestApplyPortGuard_SDEnvUntouched(t *testing.T) {
	t.Parallel()
	c := NewDefault()
	c.ApplyPortGuard()
	_, has := c.Processes["sd"].Env["OLLAMA_HOST"]
	assert.False(t, has)
	assert.Equal(t, NewDefault().Processes["sd"].Env, c.Processes["sd"].Env)
}

type fakeOptimizer struct{}

func (fakeOptimizer) DetectGPU() int                          { return 16000 }
func (fakeOptimizer) CheckXformersCompat(string, string) bool { return true }
func (fakeOptimizer) ForgeArgsForVRAM(int, bool) []string {
	return []string{"launch.py", "--listen", "--api", "--xformers"}
}

func TestApplyPortGuard_AfterBackendArgs(t *testing.T) {
	for _, auto := range []bool{false, true} {
		cfg := NewDefault()
		cfg.ActiveSD = NeoBackendKey
		b := cfg.Backends["forge"]
		b.AutoOptimize = auto
		cfg.Backends["forge"] = b
		cfg.ApplyBackendToProcess(fakeOptimizer{})
		cfg.ApplyPortGuard()
		args := cfg.Processes["sd"].Args
		assert.NotContains(t, args, "--listen", "auto=%v", auto)
		require.GreaterOrEqual(t, len(args), 2)
		assert.Equal(t, []string{"--port", "7861"}, args[len(args)-2:], "auto=%v", auto)
		assert.Equal(t, "launch.py", args[0])
	}
}

func TestApplyPortGuard_AutoStartOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		pc         ProcessConfig
		wantGuards int
	}{
		{
			"sd autostart guarded",
			ProcessConfig{
				AutoStart: true, TargetURL: "http://localhost:7860", HealthURL: "http://localhost:7860/x",
				ProxyPath: "/api/sd/", InternalPort: 7861, Args: []string{"launch.py", "--listen", "--port", "7860"},
			},
			1,
		},
		{
			"sd not autostart left alone",
			ProcessConfig{
				TargetURL: "http://localhost:7860", HealthURL: "http://localhost:7860/x",
				ProxyPath: "/api/sd/", InternalPort: 7861, Args: []string{"launch.py", "--listen", "--port", "7860"},
			},
			0,
		},
		{
			"system ollama not autostart left alone",
			ProcessConfig{
				TargetURL: "http://localhost:11434", HealthURL: "http://localhost:11434/api/tags",
				ProxyPath: "/api/llm/", InternalPort: 11435, Env: map[string]string{"OLLAMA_HOST": "0.0.0.0:11434"},
			},
			0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := guardCfg(map[string]ProcessConfig{"p": tt.pc})
			guards := c.ApplyPortGuard()
			assert.Len(t, guards, tt.wantGuards)
			if tt.wantGuards == 0 {
				assert.Equal(t, tt.pc, c.Processes["p"])
			} else {
				assert.NotEqual(t, tt.pc.TargetURL, c.Processes["p"].TargetURL)
			}
		})
	}
}

func TestApplyPortGuard_DefaultAutoStartOff(t *testing.T) {
	t.Parallel()
	c := ollamaDefault()
	o := c.Processes["ollama"]
	o.AutoStart = false
	c.Processes["ollama"] = o

	guards := c.ApplyPortGuard()
	assert.Equal(t, []PortGuard{{Process: "sd", ListenAddr: ":7860", ProxyPath: "/api/sd/"}}, guards)
	assert.Equal(t, o, c.Processes["ollama"])
	assert.Equal(t, "http://127.0.0.1:7861", c.Processes["sd"].TargetURL)
}

func TestApplyPortGuardIf_AllowCalledPerCandidate(t *testing.T) {
	t.Parallel()
	c := ollamaDefault()
	var asked []PortGuard
	guards := c.ApplyPortGuardIf(func(g PortGuard) bool {
		asked = append(asked, g)
		return true
	})
	want := []PortGuard{
		{Process: "ollama", ListenAddr: ":11434", ProxyPath: "/api/llm/"},
		{Process: "sd", ListenAddr: ":7860", ProxyPath: "/api/sd/"},
	}
	assert.ElementsMatch(t, want, asked)
	assert.Equal(t, want, guards)
}

func TestApplyPortGuardIf_Deny(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		deny       map[string]bool
		wantGuards []string
	}{
		{"deny sd", map[string]bool{"sd": true}, []string{"ollama"}},
		{"deny ollama", map[string]bool{"ollama": true}, []string{"sd"}},
		{"deny all", map[string]bool{"sd": true, "ollama": true}, nil},
		{"deny none", map[string]bool{}, []string{"ollama", "sd"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := ollamaDefault()
			before := ollamaDefault().Processes
			guarded := ollamaDefault()
			guarded.ApplyPortGuard()

			guards := c.ApplyPortGuardIf(func(g PortGuard) bool { return !tt.deny[g.Process] })

			var names []string
			for _, g := range guards {
				names = append(names, g.Process)
			}
			assert.Equal(t, tt.wantGuards, names)
			for _, key := range []string{"sd", "ollama"} {
				if tt.deny[key] {
					assert.Equal(t, before[key], c.Processes[key], key)
				} else {
					assert.Equal(t, guarded.Processes[key], c.Processes[key], key)
				}
			}
			assert.Equal(t, before["python"], c.Processes["python"])
		})
	}
}

func TestApplyPortGuardIf_NilEqualsApplyPortGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  func() *Config
	}{
		{"default", func() *Config {
			c := NewDefault()
			return &c
		}},
		{"disabled", func() *Config {
			c := NewDefault()
			c.GPU.GuardPorts = false
			return &c
		}},
		{"hand built", func() *Config {
			return guardCfg(map[string]ProcessConfig{
				"a": {AutoStart: true, TargetURL: "http://localhost:9100", HealthURL: "http://localhost:9100/h", ProxyPath: "/api/a/", InternalPort: 9101},
				"b": {TargetURL: "http://localhost:9200", ProxyPath: "/api/b/", InternalPort: 9201},
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := tt.cfg()
			b := tt.cfg()
			assert.Equal(t, a.ApplyPortGuard(), b.ApplyPortGuardIf(nil))
			assert.Equal(t, a.Processes, b.Processes)
		})
	}
}

func TestApplyPortGuardIf_Disabled(t *testing.T) {
	t.Parallel()
	c := NewDefault()
	c.GPU.GuardPorts = false
	assert.Nil(t, c.ApplyPortGuardIf(func(PortGuard) bool { return true }))
	assert.Equal(t, NewDefault().Processes, c.Processes)
}

func ollamaDefault() Config {
	c := NewDefault()
	c.ActiveLLM = LLMOllama
	return c
}
