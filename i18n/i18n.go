package i18n

import (
	"os"
	"runtime"
	"strings"
	"sync/atomic"
)

var current atomic.Value

func init() {
	current.Store(Detect(os.Getenv, uiLangID(), runtime.GOOS == "windows"))
}

func Detect(getenv func(string) string, winLangID uint16, isWindows bool) string {
	if l := Normalize(getenv("IMAGECASTER_LANG")); l != "" {
		return l
	}
	if isWindows {
		return fromLANGID(winLangID)
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(k); v != "" {
			if strings.HasPrefix(strings.ToLower(v), "ru") {
				return "ru"
			}
			return "en"
		}
	}
	return "en"
}

func Normalize(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	for _, v := range []string{"ru", "en"} {
		if strings.HasPrefix(l, v) {
			return v
		}
	}
	return ""
}

func fromLANGID(id uint16) string {
	if id&0x3ff == 0x19 {
		return "ru"
	}
	return "en"
}

func Lang() string {
	return current.Load().(string)
}

func Set(l string) {
	if l = Normalize(l); l != "" {
		current.Store(l)
	}
}

func T(key string) string {
	s, ok := dict[key]
	if !ok {
		return key
	}
	if Lang() == "ru" && s.ru != "" {
		return s.ru
	}
	return s.en
}

type pair struct{ en, ru string }
