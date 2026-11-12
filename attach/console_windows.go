package attach

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole — кнопка «Панель управления» в трее запускает этот exe из
// GUI-процесса: Go подставляет ребёнку NUL вместо stdin, хотя консольное окно
// есть (CREATE_NEW_CONSOLE). Открываем CONIN$/CONOUT$ вручную и ставим их
// стандартными дескрипторами процесса: иначе TUI-библиотеки, спрашивающие
// GetStdHandle у Windows, получают NUL и падают.
func attachConsole() bool {
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		in.Close()
		return false
	}
	os.Stdin = in
	os.Stdout = out
	setStdHandle(windows.STD_INPUT_HANDLE, in.Fd())
	setStdHandle(windows.STD_OUTPUT_HANDLE, out.Fd())
	return true
}

func setStdHandle(which uint32, fd uintptr) {
	h := windows.Handle(fd)
	// Дескрипторы переходят к Windows как есть; ошибки не критичны —
	// основной путь идёт через os.Stdin/os.Stdout.
	_ = windows.SetStdHandle(which, h)
}
