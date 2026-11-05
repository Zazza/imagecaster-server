//go:build windows

package i18n

import "golang.org/x/sys/windows"

func uiLangID() uint16 {
	r, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage").Call()
	return uint16(r)
}
