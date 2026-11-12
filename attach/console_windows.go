package attach

import "os"

// attachConsole — кнопка «Панель управления» в трее запускает этот exe из
// GUI-процесса: Go подставляет ребёнку NUL вместо stdin, хотя консольное окно
// есть (CREATE_NEW_CONSOLE). Открываем его CONIN$/CONOUT$ вручную.
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
	return true
}
