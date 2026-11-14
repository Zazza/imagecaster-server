package process

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenRotatedLog(t *testing.T) {
	cases := []struct {
		name       string
		current    string
		backup     string
		max        int64
		wantCur    string
		wantBackup string
	}{
		{name: "small file is appended", current: "abc", max: 10, wantCur: "abcX"},
		{name: "no file is created", max: 10, wantCur: "X"},
		{name: "big file moves to backup", current: "0123456789AB", max: 10, wantCur: "X", wantBackup: "0123456789AB"},
		{name: "old backup is replaced", current: "0123456789AB", backup: "old", max: 10, wantCur: "X", wantBackup: "0123456789AB"},
		{name: "write over limit rotates", current: "0123456789", max: 10, wantCur: "X", wantBackup: "0123456789"},
		{name: "write up to limit is appended", current: "012345678", max: 10, wantCur: "012345678X"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "x.log")
			if tc.current != "" {
				require.NoError(t, os.WriteFile(p, []byte(tc.current), 0o644))
			}
			if tc.backup != "" {
				require.NoError(t, os.WriteFile(p+".1", []byte(tc.backup), 0o644))
			}
			f, err := OpenRotatingFile(p, tc.max)
			require.NoError(t, err)
			_, err = f.Write([]byte("X"))
			require.NoError(t, err)
			require.NoError(t, f.Close())

			got, err := os.ReadFile(p)
			require.NoError(t, err)
			assert.Equal(t, tc.wantCur, string(got))
			b, err := os.ReadFile(p + ".1")
			if tc.wantBackup == "" {
				assert.True(t, os.IsNotExist(err), "unexpected backup %q", b)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantBackup, string(b))
		})
	}
}

func TestPipeLogs(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantLine string
		wantNote bool
	}{
		{name: "line over 64KB is logged", input: strings.Repeat("a", 100<<10) + "\nnext\n", wantLine: "next"},
		{name: "line over 1MB stops logging and drains", input: "first\n" + strings.Repeat("b", 2<<20) + "\nafter\n" + strings.Repeat("c", 1<<20), wantLine: "first", wantNote: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			buf := NewRingBuffer(10)
			lf, err := OpenRotatingFile(filepath.Join(t.TempDir(), "p.log"), MaxLogFileSize)
			require.NoError(t, err)
			t.Cleanup(func() { lf.Close() })
			done := make(chan struct{})
			go func() {
				pipeLogs(pr, buf, lf, "t")
				close(done)
			}()

			wrote := make(chan error, 1)
			go func() {
				_, err := io.Copy(pw, bytes.NewReader([]byte(tc.input)))
				pw.Close()
				wrote <- err
			}()
			select {
			case err := <-wrote:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("writer blocked: output is not read")
			}
			<-done

			lines := strings.Join(buf.Lines(10), "\n")
			assert.Contains(t, lines, tc.wantLine)
			if tc.wantNote {
				assert.Contains(t, lines, "output not logged any more")
				assert.NotContains(t, lines, "after")
			}
		})
	}
}

func TestRotatingFileRotatesWhileWriting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "svc.log")
	f, err := OpenRotatingFile(p, 1000)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })

	line := strings.Repeat("x", 99) + "\n"
	for range 25 {
		_, err := f.Write([]byte(line))
		require.NoError(t, err)
	}
	cur, err := os.Stat(p)
	require.NoError(t, err)
	bak, err := os.Stat(p + ".1")
	require.NoError(t, err)
	assert.LessOrEqual(t, cur.Size(), int64(1000))
	assert.LessOrEqual(t, bak.Size(), int64(1000))
	assert.Equal(t, int64(500), cur.Size())

	require.NoError(t, f.Close())
	_, err = f.Write([]byte("late"))
	assert.ErrorIs(t, err, os.ErrClosed)
}

func TestPipeLogsRotatesLongRunningLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "svc.log")
	lf, err := OpenRotatingFile(p, 2000)
	require.NoError(t, err)
	t.Cleanup(func() { lf.Close() })
	var in strings.Builder
	for i := range 100 {
		in.WriteString(strings.Repeat("y", 50))
		in.WriteString(string(rune('a' + i%26)))
		in.WriteString("\n")
	}
	pipeLogs(strings.NewReader(in.String()), NewRingBuffer(10), lf, "t")
	cur, err := os.Stat(p)
	require.NoError(t, err)
	assert.LessOrEqual(t, cur.Size(), int64(2000))
	assert.FileExists(t, p+".1")
}
