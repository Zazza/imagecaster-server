package sdcpp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidSampler(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"", true},
		{"euler", true},
		{"euler_a", true},
		{"dpm++2m_sde", true},
		{"Euler a", false},
		{"DPM++ 2M Karras", false},
		{"nope", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.ok, validSampler(tc.in), tc.in)
	}
}

func TestValidScheduler(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"", true},
		{"karras", true},
		{"discrete", true},
		{"Karras", false},
		{"Automatic", false},
		{"nope", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.ok, validScheduler(tc.in), tc.in)
	}
}
