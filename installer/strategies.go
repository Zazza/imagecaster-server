package installer

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"imagecaster-server/config"
	"imagecaster-server/process"
)

func (inst *Installer) installZip(key string, ic config.InstallConfig, lb *process.RingBuffer) error {
	target := inst.resolveTarget(ic.Target)
	if inst.needsBackendSetup(key) {
		if st, err := os.Stat(target); err == nil && st.IsDir() {
			lb.Write("Backend files found, finishing setup in " + target)
			return inst.backendSetup(target, lb)
		}
	}
	inst.setProgress(key, "downloading")
	lb.Write(fmt.Sprintf("Downloading %s -> %s", ic.URL, target))
	log.Printf("[%s] downloading %s -> %s", key, ic.URL, target)

	resp, err := http.Get(ic.URL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "sd-install-*.zip")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	pw := &progressWriter{
		w:        tmpFile,
		total:    resp.ContentLength,
		key:      key,
		target:   ic.Target,
		lastLog:  0,
		lastTime: 0,
		inst:     inst,
		lb:       lb,
	}
	size, err := io.Copy(pw, resp.Body)
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("save download: %w", err)
	}
	lb.Write(fmt.Sprintf("Downloaded %s", FormatBytes(size)))
	log.Printf("[%s] downloaded %s", key, FormatBytes(size))

	inst.setProgress(key, "extracting")
	lb.Write(fmt.Sprintf("Extracting to %s", target))
	log.Printf("[%s] extracting to %s", key, target)

	if err := extractZip(tmpPath, target); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	if inst.needsBackendSetup(key) {
		if err := inst.backendSetup(target, lb); err != nil {
			return err
		}
	}

	lb.Write("Installation complete")
	log.Printf("[%s] installed to %s", key, target)
	return nil
}

func (inst *Installer) needsBackendSetup(key string) bool {
	_, ok := inst.config.Backends[key]
	return ok && key == config.NeoBackendKey
}
