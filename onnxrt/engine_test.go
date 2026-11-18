package onnxrt

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLibCandidatesNoRelativePaths(t *testing.T) {
	for _, p := range LibCandidates("") {
		if filepath.IsAbs(p) {
			continue
		}
		assert.Equal(t, p, filepath.Base(p), "candidate %q must be absolute or a bare name for the system loader", p)
		assert.NotContains(t, p, "lib/")
	}
	assert.Equal(t, []string{"/x/libonnxruntime.so"}, LibCandidates("/x/libonnxruntime.so"))
}

func TestSharedMissingLibrary(t *testing.T) {
	_, err := Shared(filepath.Join(t.TempDir(), "libonnxruntime.so"))
	assert.Error(t, err)
}
