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
		{"default", func(c *config.Config) {}, []string{"python", "forge", "ollama"}},
		{"sd off", func(c *config.Config) { setAuto(c, "sd", false) }, []string{"ollama"}},
		{"ollama off", func(c *config.Config) { setAuto(c, "ollama", false) }, []string{"python", "forge"}},
		{"both off", func(c *config.Config) { setAuto(c, "sd", false); setAuto(c, "ollama", false) }, nil},
		{"custom backend", func(c *config.Config) {
			b := c.Backends["forge"]
			b.ProcessKey = "sd"
			c.Backends["forge-neo"] = b
			c.ActiveSD = "forge-neo"
		}, []string{"python", "forge-neo", "ollama"}},
		{"missing backend", func(c *config.Config) { c.ActiveSD = "nope" }, []string{"ollama"}},
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

func setAuto(c *config.Config, key string, v bool) {
	p := c.Processes[key]
	p.AutoStart = v
	c.Processes[key] = p
}
