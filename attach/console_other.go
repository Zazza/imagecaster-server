//go:build !windows

package attach

// На прочих ОС stdin наследуется терминалом обычным порядком.
func attachConsole() bool { return false }
