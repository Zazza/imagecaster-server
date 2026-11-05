package tui

import (
	"strings"

	"imagecaster-server/i18n"
)

func T(key string) string { return i18n.T(key) }

func tOr(key, fallback string) string {
	if s := T(key); s != key {
		return s
	}
	return fallback
}

func statusLabel(status string) string {
	return tOr("status."+status, status)
}

func progressLabel(p string) string {
	if rest, ok := strings.CutPrefix(p, "Downloading "); ok {
		return strings.Replace(T("progress.download_of"), "%s", rest, 1)
	}
	return tOr("progress."+p, p)
}
