package llamacpp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseKeepAlive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      any
		want    time.Duration
		wantErr bool
	}{
		{name: "float seconds", in: float64(30), want: 30 * time.Second},
		{name: "fractional seconds", in: 1.5, want: 1500 * time.Millisecond},
		{name: "int seconds", in: 45, want: 45 * time.Second},
		{name: "zero unloads now", in: float64(0), want: 0},
		{name: "minus one forever", in: float64(-1), want: -1},
		{name: "any negative forever", in: -300, want: -1},
		{name: "json number", in: json.Number("120"), want: 2 * time.Minute},
		{name: "json number negative", in: json.Number("-1"), want: -1},
		{name: "json number bad", in: json.Number("ten"), wantErr: true},
		{name: "duration string", in: "5m", want: 5 * time.Minute},
		{name: "compound duration", in: "1h30m", want: 90 * time.Minute},
		{name: "duration padded", in: "  10s ", want: 10 * time.Second},
		{name: "number in string", in: "60", want: time.Minute},
		{name: "zero string", in: "0", want: 0},
		{name: "negative number string", in: "-1", want: -1},
		{name: "negative duration string", in: "-5m", want: -1},
		{name: "empty string", in: "", wantErr: true},
		{name: "blank string", in: "   ", wantErr: true},
		{name: "unknown unit", in: "5 minutes", wantErr: true},
		{name: "bool", in: true, wantErr: true},
		{name: "object", in: map[string]any{"s": 1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseKeepAlive(tt.in)
			if !tt.wantErr {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				return
			}
			require.Error(t, err)
			var he *httpError
			require.True(t, errors.As(err, &he), "got %T", err)
			assert.Equal(t, http.StatusBadRequest, he.code)
			assert.Contains(t, err.Error(), "invalid keep_alive")
			assert.Contains(t, err.Error(), fmt.Sprint(tt.in))
		})
	}
}
