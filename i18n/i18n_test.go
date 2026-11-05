package i18n

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		id    uint16
		isWin bool
		want  string
	}{
		{"nothing", nil, 0, false, "en"},
		{"override ru", map[string]string{"IMAGECASTER_LANG": "ru"}, 0, false, "ru"},
		{"override normalized", map[string]string{"IMAGECASTER_LANG": "ru_RU.UTF-8"}, 0, false, "ru"},
		{"override beats LANG", map[string]string{"IMAGECASTER_LANG": "en", "LANG": "ru_RU.UTF-8"}, 0, false, "en"},
		{"override beats windows", map[string]string{"IMAGECASTER_LANG": "en"}, 0x0419, true, "en"},
		{"windows ru", nil, 0x0419, true, "ru"},
		{"windows ru other sublang", nil, 0x0819, true, "ru"},
		{"windows en", nil, 0x0409, true, "en"},
		{"windows id ignored on unix", nil, 0x0419, false, "en"},
		{"windows ru beats LANG", map[string]string{"LANG": "en_US.UTF-8"}, 0x0419, true, "ru"},
		{"windows uses only LANGID", map[string]string{"LANG": "ru_RU.UTF-8"}, 0x0409, true, "en"},
		{"LANG ru", map[string]string{"LANG": "ru_RU.UTF-8"}, 0, false, "ru"},
		{"LANG en", map[string]string{"LANG": "en_US.UTF-8"}, 0, false, "en"},
		{"LC_MESSAGES beats LANG", map[string]string{"LC_MESSAGES": "ru_RU.UTF-8", "LANG": "en_US.UTF-8"}, 0, false, "ru"},
		{"LC_ALL beats LC_MESSAGES", map[string]string{"LC_ALL": "en_US.UTF-8", "LC_MESSAGES": "ru_RU.UTF-8"}, 0, false, "en"},
		{"LC_ALL ru", map[string]string{"LC_ALL": "ru_RU.UTF-8", "LANG": "en_US.UTF-8"}, 0, false, "ru"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Detect(env(tt.env), tt.id, tt.isWin))
		})
	}
}

func TestSetLangT(t *testing.T) {
	prev := Lang()
	t.Cleanup(func() { Set(prev) })

	Set("en")
	assert.Equal(t, "en", Lang())
	en := T("wizard.title")
	Set("ru")
	assert.Equal(t, "ru", Lang())
	ru := T("wizard.title")

	assert.NotEmpty(t, en)
	assert.NotEmpty(t, ru)
	assert.NotEqual(t, en, ru)
	assert.Equal(t, dict["wizard.title"].en, en)
	assert.Equal(t, dict["wizard.title"].ru, ru)
}

func TestTUnknownKey(t *testing.T) {
	prev := Lang()
	t.Cleanup(func() { Set(prev) })

	for _, l := range []string{"en", "ru"} {
		Set(l)
		assert.Equal(t, "no.such.key", T("no.such.key"))
	}
}
