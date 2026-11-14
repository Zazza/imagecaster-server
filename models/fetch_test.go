package models

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadPartAlreadyComplete(t *testing.T) {
	t.Parallel()
	content := payload(128 << 10)
	corrupt := append([]byte(nil), content...)
	corrupt[len(corrupt)/2] ^= 0xff
	tests := []struct {
		name     string
		part     []byte
		sha      string
		wantDone bool
		wantErr  string
		wantFile bool
	}{
		{name: "complete, no sha", part: content, wantDone: true, wantFile: true},
		{name: "complete, sha matches", part: content, sha: sum(content), wantDone: true, wantFile: true},
		{name: "complete, sha differs", part: corrupt, sha: sum(content), wantErr: "sha256 mismatch"},
		{name: "part larger than source", part: append(append([]byte(nil), content...), 1, 2, 3), wantErr: "status 416"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &origin{content: content}
			srv := httptest.NewServer(o)
			defer srv.Close()
			dir := t.TempDir()
			mm := newTestModelManager(t, dir)
			dest := filepath.Join(dir, "m.safetensors")
			require.NoError(t, os.WriteFile(dest+".part", tt.part, 0o644))

			res := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", tt.sha)
			assert.Equal(t, tt.wantDone, res.done, res.err)
			if tt.wantErr != "" {
				assert.Contains(t, res.err, tt.wantErr)
			}
			assert.Equal(t, int32(1), o.hits.Load())
			assert.Equal(t, []string{"bytes=" + strconv.Itoa(len(tt.part)) + "-"}, o.rangeHeaders())
			assert.NoFileExists(t, dest+".part")
			if tt.wantFile {
				got, err := os.ReadFile(dest)
				require.NoError(t, err)
				assert.Equal(t, sum(content), sum(got))
			} else {
				assert.NoFileExists(t, dest)
			}
		})
	}
}

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
