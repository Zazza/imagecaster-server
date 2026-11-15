package process

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func (pm *ProcessManager) openLogFile(name string, mp *ManagedProcess) {
	if mp.LogFile != nil {
		mp.LogFile.Close()
		mp.LogFile = nil
	}
	logsDir := filepath.Join(pm.dataDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		log.Printf("[%s] log dir: %v", name, err)
		return
	}
	f, err := OpenRotatingFile(filepath.Join(logsDir, name+".log"), MaxLogFileSize)
	if err != nil {
		log.Printf("[%s] log file: %v", name, err)
		return
	}
	mp.LogFile = f
}

func (pm *ProcessManager) Logs(name string, lines int) ([]string, error) {
	pm.Mu.RLock()
	mp, ok := pm.processes[name]
	pm.Mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("process %q %w", name, ErrNotFound)
	}
	return mp.LogBuf.Lines(lines), nil
}
