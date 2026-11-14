package desktop

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCachedLANIPs(t *testing.T) {
	calls := 0
	old := lanIPsSource
	lanIPsSource = func() []string {
		calls++
		return []string{"192.168.1.5"}
	}
	t.Cleanup(func() {
		lanIPsSource = old
		lanIPs.at, lanIPs.ips = time.Time{}, nil
	})
	lanIPs.at = time.Time{}

	steps := []struct {
		name      string
		age       time.Duration
		wantCalls int
	}{
		{name: "first call reads interfaces", wantCalls: 1},
		{name: "within TTL is cached", age: 10 * time.Second, wantCalls: 1},
		{name: "after TTL reads again", age: 31 * time.Second, wantCalls: 2},
	}
	for _, s := range steps {
		if s.age > 0 {
			lanIPs.at = time.Now().Add(-s.age)
		}
		got := CachedLANIPs()
		assert.Equal(t, []string{"192.168.1.5"}, got, s.name)
		assert.Equal(t, s.wantCalls, calls, s.name)
		got[0] = "changed"
	}
}
