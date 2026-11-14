package llamacpp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunningNamesCachedForTTL(t *testing.T) {
	e := newFacadeEnv(t)
	ids := e.f.IDs()
	require.NotEmpty(t, ids)
	var name, id string
	for n, i := range ids {
		name, id = n, i
		break
	}
	gets := func() int {
		e.fr.mu.Lock()
		defer e.fr.mu.Unlock()
		return e.fr.modelGets
	}
	setStatus := func(st string) {
		e.fr.mu.Lock()
		e.fr.status[id] = st
		e.fr.mu.Unlock()
	}

	setStatus("loaded")
	base := gets()
	steps := []struct {
		name      string
		advance   time.Duration
		status    string
		wantNames []string
		wantGets  int
	}{
		{name: "first call asks router", wantNames: []string{name}, wantGets: 1},
		{name: "second call is cached", advance: time.Second, status: "unloaded", wantNames: []string{name}, wantGets: 1},
		{name: "after TTL asks again", advance: 2 * time.Second, wantNames: nil, wantGets: 2},
	}
	for _, s := range steps {
		e.advance(s.advance)
		if s.status != "" {
			setStatus(s.status)
		}
		assert.Equal(t, s.wantNames, e.f.RunningNames(), s.name)
		assert.Equal(t, s.wantGets, gets()-base, s.name)
	}
}
