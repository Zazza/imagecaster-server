package config

import (
	"sync"
	"testing"
)

func TestProcessAccessConcurrent(t *testing.T) {
	c := &Config{Processes: map[string]ProcessConfig{"ollama": {Name: "ollama"}, "sd": {Name: "sd"}}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				c.SetProcess("ollama", ProcessConfig{Name: "ollama", Binary: "/x/ollama"})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if _, ok := c.Process("sd"); !ok {
					t.Error("sd missing")
					return
				}
				for range c.ProcessesCopy() {
				}
			}
		}()
	}
	wg.Wait()
	if pc, _ := c.Process("ollama"); pc.Binary != "/x/ollama" {
		t.Fatalf("binary = %q", pc.Binary)
	}
}
