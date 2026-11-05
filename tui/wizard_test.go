package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wizardSubmit(t *testing.T, typed string) WizardDoneMsg {
	t.Helper()
	var m tea.Model = NewWizardModel("/default/data")
	if typed != "" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(typed)})
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	msg, ok := cmd().(WizardDoneMsg)
	require.True(t, ok)
	return msg
}

func TestWizardEnter(t *testing.T) {
	tests := []struct {
		name  string
		typed string
		want  string
	}{
		{"empty uses default", "", "/default/data"},
		{"typed value", "/srv/imgdata", "/srv/imgdata"},
		{"typed value trimmed", "  /srv/imgdata  ", "/srv/imgdata"},
		{"only spaces uses default", "   ", "/default/data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, WizardDoneMsg{DataDir: tt.want}, wizardSubmit(t, tt.typed))
		})
	}
}

func TestWizardResultOnlyDataDir(t *testing.T) {
	assert.Equal(t, WizardResult{DataDir: "/x"}, WizardResult{DataDir: "/x"})
	var r WizardResult
	assert.Equal(t, WizardResult{}, r)
}

func TestInstallOrder(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{"empty", nil, nil},
		{"only ollama", []string{"ollama"}, []string{"ollama"}},
		{"ollama last", []string{"ollama", "forge-neo"}, []string{"forge-neo", "ollama"}},
		{"sorted rest", []string{"zeta", "ollama", "alpha", "forge-neo"}, []string{"alpha", "forge-neo", "zeta", "ollama"}},
		{"no ollama", []string{"b", "a"}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := map[string]ComponentInstallStatus{}
			for _, k := range tt.keys {
				st[k] = ComponentInstallStatus{Key: k}
			}
			got := installOrder(st)
			if tt.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
