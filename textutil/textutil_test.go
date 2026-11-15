package textutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc"},
		{"привет", 6, "привет"},
		{"привет", 3, "при"},
		{"", 0, ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Truncate(tt.in, tt.max), tt.in)
	}
}

func TestEllipsis(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 5, "abc"},
		{"abcdefgh", 6, "abc..."},
		{"приветмир", 7, "прив..."},
		{"abcdef", 3, "abc"},
		{"abcdef", 2, "ab"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Ellipsis(tt.in, tt.max), tt.in)
	}
}
