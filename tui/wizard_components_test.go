package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWizardActiveComponents(t *testing.T) {
	got := NewWizardModel(t.TempDir()).ActiveComponents()
	assert.Equal(t, map[string]bool{"forge": true, "ollama": true}, got)
}

func TestInstallOrder(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{"empty", nil, []string{"python", "ollama"}},
		{"full", []string{"ollama", "zeta", "forge", "python", "alpha"}, []string{"python", "alpha", "forge", "zeta", "ollama"}},
		{"no python ollama", []string{"forge-neo", "forge"}, []string{"python", "forge", "forge-neo", "ollama"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]ComponentInstallStatus{}
			for _, k := range tt.keys {
				m[k] = ComponentInstallStatus{Key: k}
			}
			assert.Equal(t, tt.want, installOrder(m))
		})
	}
}
