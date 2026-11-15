package dl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContentRangeTotal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in     string
		want   int64
		wantOK bool
	}{
		{in: "bytes */1048576", want: 1048576, wantOK: true},
		{in: "bytes 0-99/200", want: 200, wantOK: true},
		{in: "bytes 0-99/*", wantOK: false},
		{in: "bytes 0-99", wantOK: false},
		{in: "", wantOK: false},
		{in: "bytes */abc", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, ok := contentRangeTotal(tt.in)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
