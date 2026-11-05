package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOllamaDefaultEnvNoDebug(t *testing.T) {
	_, ok := NewDefault().Processes["ollama"].Env["OLLAMA_DEBUG"]
	assert.False(t, ok)
}
