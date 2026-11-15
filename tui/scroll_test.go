package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScrollViewKeys(t *testing.T) {
	tests := []struct {
		name          string
		start         scrollView
		key           string
		wantOffset    int
		wantFollowing bool
	}{
		{"up stops following", scrollView{offset: 5, following: true}, "up", 4, false},
		{"up at top", scrollView{offset: 0}, "k", 0, false},
		{"down to bottom follows", scrollView{offset: 9}, "down", 10, true},
		{"down at bottom", scrollView{offset: 10}, "j", 10, true},
		{"pgup clamps", scrollView{offset: 2, following: true}, "pgup", 0, false},
		{"pgdown clamps and follows", scrollView{offset: 8}, "pgdown", 10, true},
		{"pgdown middle", scrollView{offset: 1}, "pgdown", 4, false},
		{"g top", scrollView{offset: 7, following: true}, "g", 0, false},
		{"G bottom", scrollView{offset: 1}, "G", 10, true},
		{"other key", scrollView{offset: 3}, "x", 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.start
			s.handleKey(tt.key, 10, 3)
			assert.Equal(t, tt.wantOffset, s.offset)
			assert.Equal(t, tt.wantFollowing, s.following)
		})
	}
}

func TestMaxScrollOffset(t *testing.T) {
	assert.Equal(t, 0, maxScrollOffset(3, 10))
	assert.Equal(t, 0, maxScrollOffset(10, 10))
	assert.Equal(t, 5, maxScrollOffset(15, 10))
}
