package installer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"imagecaster-server/config"
)

func TestInstallPlan(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *config.Config)
		want   []string
	}{
		{"default", func(c *config.Config) {}, []string{config.SDCppBackendKey, config.LlamaCppProcessKey}},
		{"sd off", func(c *config.Config) { setAuto(c, "sd", false) }, []string{config.LlamaCppProcessKey}},
		{"llm off", func(c *config.Config) { setAuto(c, config.LlamaCppProcessKey, false) }, []string{config.SDCppBackendKey}},
		{"both off", func(c *config.Config) { setAuto(c, "sd", false); setAuto(c, config.LlamaCppProcessKey, false) }, nil},
		{"custom backend", func(c *config.Config) {
			b := c.Backends[config.NeoBackendKey]
			c.Backends["custom"] = b
			c.ActiveSD = "custom"
		}, []string{"custom", config.LlamaCppProcessKey}},
		{"custom backend own process", func(c *config.Config) {
			b := c.Backends[config.NeoBackendKey]
			b.ProcessKey = "sd2"
			c.Backends["custom"] = b
			c.ActiveSD = "custom"
			c.Processes["sd2"] = config.ProcessConfig{AutoStart: true}
			setAuto(c, "sd", false)
		}, []string{"custom", config.LlamaCppProcessKey}},
		{"custom backend process off", func(c *config.Config) {
			b := c.Backends[config.NeoBackendKey]
			b.ProcessKey = "sd2"
			c.Backends["custom"] = b
			c.ActiveSD = "custom"
			c.Processes["sd2"] = config.ProcessConfig{AutoStart: false}
		}, []string{config.LlamaCppProcessKey}},
		{"rollback to ollama", func(c *config.Config) { c.ActiveLLM = config.LLMOllama }, []string{config.SDCppBackendKey, "ollama"}},
		{"missing backend", func(c *config.Config) { c.ActiveSD = "nope" }, []string{config.LlamaCppProcessKey}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewDefault()
			tt.mutate(&cfg)
			got := NewInstaller(&cfg).installPlan()
			if tt.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultConfigBackends(t *testing.T) {
	cfg := config.NewDefault()
	assert.Equal(t, "forge-neo", config.NeoBackendKey)
	assert.Equal(t, "2.31", config.NeoVersion)
	assert.Equal(t, config.SDCppBackendKey, cfg.ActiveSD)
	assert.Len(t, cfg.Backends, 2)
	b, ok := cfg.Backends[config.NeoBackendKey]
	assert.True(t, ok)
	assert.Equal(t, config.InstallZip, b.Install.Method)
	assert.Equal(t, config.InstallRelease, cfg.Backends[config.SDCppBackendKey].Install.Method)
	assert.NotContains(t, cfg.Processes, "python")
	assert.NotContains(t, cfg.Backends, "forge")
}

func setAuto(c *config.Config, key string, v bool) {
	p := c.Processes[key]
	p.AutoStart = v
	c.Processes[key] = p
}
