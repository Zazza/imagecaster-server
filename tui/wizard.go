package tui

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type WizardDoneMsg struct {
	DataDir string
}

type WizardResult struct {
	DataDir string
}

type WizardModel struct {
	dataInput textinput.Model
	submitted bool
}

func NewWizardModel(defaultDir string) WizardModel {
	ti := textinput.New()
	ti.Placeholder = defaultDir
	ti.Focus()
	ti.CharLimit = 256
	ti.Width = 40
	return WizardModel{dataInput: ti}
}

func (m WizardModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m WizardModel) dir() string {
	if v := strings.TrimSpace(m.dataInput.Value()); v != "" {
		return v
	}
	return m.dataInput.Placeholder
}

func (m WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyEnter {
		m.submitted = true
		dir := m.dir()
		return m, func() tea.Msg { return WizardDoneMsg{DataDir: dir} }
	}
	var cmd tea.Cmd
	m.dataInput, cmd = m.dataInput.Update(msg)
	return m, cmd
}

func (m WizardModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" " + T("wizard.title") + " "))
	b.WriteString("\n\n")
	b.WriteString(labelStyle.Render(T("wizard.datadir")))
	b.WriteString("\n")
	b.WriteString(selectedStyle.Render("> " + m.dataInput.View()))
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render(T("wizard.components")))
	if runtime.GOOS == "windows" {
		if abs, err := filepath.Abs(m.dir()); err == nil && strings.HasPrefix(strings.ToUpper(abs), "C:") {
			b.WriteString("\n")
			b.WriteString(progressYellow.Render(T("wizard.cdrive")))
		}
	}
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render(T("wizard.help")))
	return borderStyle.Render(b.String())
}

type wizardRunner struct {
	wizard WizardModel
	result WizardResult
	done   bool
}

func (m *wizardRunner) Init() tea.Cmd {
	return m.wizard.Init()
}

func (m *wizardRunner) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case WizardDoneMsg:
		m.result = WizardResult{DataDir: msg.DataDir}
		m.done = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.done = true
			return m, tea.Quit
		}
	}
	w, cmd := m.wizard.Update(msg)
	m.wizard = w.(WizardModel)
	return m, cmd
}

func (m *wizardRunner) View() string {
	if m.done {
		return ""
	}
	return m.wizard.View()
}

func RunWizard(defaultDir string) WizardResult {
	runner := &wizardRunner{wizard: NewWizardModel(defaultDir)}
	p := tea.NewProgram(runner, tea.WithAltScreen())
	if final, err := p.Run(); err == nil {
		if r, ok := final.(*wizardRunner); ok {
			return r.result
		}
	}
	return WizardResult{DataDir: defaultDir}
}
