package attach

import (
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/term"
)

// Кнопка «Панель управления» в трее запускает exe с NUL вместо stdin:
// не-терминальный stdin должен подхватить CONIN$ своей консоли.
func TestAttachConsoleReplacesNullStdin(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin уже терминал — сценарий запуска из трея не воспроизводится")
	}
	prevIn, prevOut := os.Stdin, os.Stdout
	t.Cleanup(func() { os.Stdin, os.Stdout = prevIn, prevOut })

	ok := attachConsole()
	if !ok {
		t.Skip("консольного окна нет (GUI-контекст) — здесь ошибиться нечем")
	}
	assert.True(t, term.IsTerminal(int(os.Stdin.Fd())), "CONIN$ даёт терминал")
}
