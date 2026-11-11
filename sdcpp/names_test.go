package sdcpp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitSampler(t *testing.T) {
	cases := []struct {
		in, sampler, scheduler string
		ok                     bool
	}{
		{"Euler", "euler", "", true},
		{"Euler Simple", "euler", "simple", true},
		{"Euler a Karras", "euler_a", "karras", true},
		{"Euler Karras", "euler", "karras", true},
		{"DPM++ 2M Karras", "dpm++2m", "karras", true},
		{"DPM++ 2M Exponential", "dpm++2m", "exponential", true},
		{"DPM++ 2M SDE Karras", "dpm++2m_sde", "karras", true},
		{"DPM++ 2M SDE", "dpm++2m_sde", "", true},
		{"Karras", "", "", false},
		{"Foo Simple", "", "", false},
	}
	for _, tc := range cases {
		s, sc, ok := SplitSampler(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		assert.Equal(t, tc.sampler, s, tc.in)
		assert.Equal(t, tc.scheduler, sc, tc.in)
	}
}
