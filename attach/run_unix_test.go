//go:build unix

package attach

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withTerminalStdin(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot allocate pty: %v", err)
	}
	swapStdin(t, f)
	t.Cleanup(func() { f.Close() })
}

func TestMain_TooManyArgs(t *testing.T) {
	withTerminalStdin(t)
	err := Main([]string{"a", "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestMain_Unreachable(t *testing.T) {
	withTerminalStdin(t)
	start := time.Now()
	err := Main([]string{"127.0.0.1:1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unreachable")
	assert.Contains(t, err.Error(), "http://127.0.0.1:1")
	assert.Less(t, time.Since(start), preflightTimeout, "preflight must fail fast")
}

func TestMain_BadTarget(t *testing.T) {
	withTerminalStdin(t)
	err := Main([]string{"https://x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported target")
}

func TestMain_HelpArg(t *testing.T) {
	withTerminalStdin(t)
	for _, arg := range []string{"-h", "--help"} {
		err := Main([]string{arg})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage")
	}
}
