package tui

import (
	"os"
	"testing"

	"imagecaster-server/i18n"
)

func TestMain(m *testing.M) {
	i18n.Set("en")
	os.Exit(m.Run())
}
