package installer

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"imagecaster-server/hide"
	"imagecaster-server/process"
)

func findOllamaBinary(dataDir string) string {
	if dataDir != "" {
		binName := "ollama"
		if runtime.GOOS == "windows" {
			binName = "ollama.exe"
		}
		for _, p := range []string{
			filepath.Join(dataDir, "ollama", "bin", binName),
			filepath.Join(dataDir, "ollama", binName),
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData != "" {
			path := filepath.Join(localAppData, "Programs", "Ollama", "ollama.exe")
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
		home, _ := os.UserHomeDir()
		if home != "" {
			path := filepath.Join(home, "AppData", "Local", "Programs", "Ollama", "ollama.exe")
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	if runtime.GOOS == "linux" {
		for _, p := range []string{"/usr/local/bin/ollama", "/usr/bin/ollama"} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if runtime.GOOS == "darwin" {
		for _, p := range []string{"/usr/local/bin/ollama", "/opt/homebrew/bin/ollama"} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if p, err := exec.LookPath("ollama"); err == nil {
		return p
	}
	return ""
}

func (inst *Installer) installOllama() (string, error) {
	if p := findOllamaBinary(inst.config.DataDir); p != "" {
		return p, nil
	}

	lb := process.NewRingBuffer(50)
	if err := inst.installOllamaSystem(lb); err != nil {
		for _, line := range lb.Lines(50) {
			if line != "" {
				log.Printf("[ollama-install] %s", line)
			}
		}
		return "", err
	}

	for _, line := range lb.Lines(50) {
		if line != "" {
			log.Printf("[ollama-install] %s", line)
		}
	}

	return findOllamaBinary(inst.config.DataDir), nil
}

func (inst *Installer) OllamaFound() bool {
	if pc, ok := inst.config.Process("ollama"); ok && filepath.IsAbs(pc.Binary) {
		if _, err := os.Stat(pc.Binary); err == nil {
			return true
		}
	}
	return findOllamaBinary(inst.config.DataDir) != ""
}

func (inst *Installer) EnsureOllama() string {
	p, err := inst.installOllama()
	if err != nil {
		log.Printf("[ollama] install failed: %v", err)
		return ""
	}
	if p != "" {
		log.Printf("[ollama] found at %s", p)
	}
	return p
}

func (inst *Installer) installOllamaSystem(lb *process.RingBuffer) error {
	switch runtime.GOOS {
	case "windows":
		return inst.installOllamaWindows(lb)
	case "linux":
		return inst.installOllamaLinux(lb)
	case "darwin":
		return inst.installOllamaMac(lb)
	}
	return fmt.Errorf("unsupported platform for Ollama auto-install")
}

func (inst *Installer) installOllamaWindows(lb *process.RingBuffer) error {
	return inst.downloadOllamaBinary(lb, "windows")
}

func (inst *Installer) installOllamaLinux(lb *process.RingBuffer) error {
	return inst.downloadOllamaBinary(lb, "linux")
}

func (inst *Installer) installOllamaMac(lb *process.RingBuffer) error {
	if brew, err := exec.LookPath("brew"); err == nil {
		lb.Write("Installing Ollama via Homebrew...")
		log.Printf("[ollama] using Homebrew at %s", brew)

		cmd := hide.Command(brew, "install", "ollama")
		output, err := cmd.CombinedOutput()
		if len(output) > 0 {
			for _, line := range strings.Split(string(output), "\n") {
				if line != "" {
					lb.Write(line)
				}
			}
		}
		if err != nil {
			return fmt.Errorf("brew install ollama: %w", err)
		}

		lb.Write("Ollama installed via Homebrew")
		return nil
	}

	lb.Write("Homebrew not found, downloading binary...")
	return inst.downloadOllamaBinary(lb, "darwin")
}

func (inst *Installer) downloadOllamaBinary(lb *process.RingBuffer, goos string) error {
	arch := runtime.GOARCH
	if arch != "arm64" {
		arch = "amd64"
	}

	ext := ".tgz"
	switch goos {
	case "linux":
		ext = ".tar.zst"
	case "windows":
		ext = ".zip"
	}
	url := fmt.Sprintf("https://ollama.com/download/ollama-%s-%s%s", goos, arch, ext)
	if goos == "darwin" {
		url = "https://ollama.com/download/ollama-darwin.tgz"
	}
	targetDir := filepath.Join(inst.config.DataDir, "ollama")

	inst.setProgress("ollama", "downloading")
	lb.Write(fmt.Sprintf("Downloading Ollama (%s/%s)...", goos, arch))
	log.Printf("[ollama] downloading %s -> %s", url, targetDir)

	resp, err := HTTPGet(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "ollama-download-*"+ext)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	pw := &progressWriter{
		w:        tmpFile,
		total:    resp.ContentLength,
		key:      "ollama",
		target:   targetDir,
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

	inst.setProgress("ollama", "extracting")
	lb.Write(fmt.Sprintf("Downloaded %s, extracting...", FormatBytes(size)))

	partDir := targetDir + ".part"
	if err := os.RemoveAll(partDir); err != nil {
		return fmt.Errorf("clean partial dir: %w", err)
	}
	if err := os.MkdirAll(partDir, 0755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}
	defer os.RemoveAll(partDir)

	switch {
	case strings.HasSuffix(ext, ".tar.zst"):
		err = extractOllamaTarZst(tmpPath, partDir, lb)
	case strings.HasSuffix(ext, ".tgz"):
		err = extractOllamaTgz(tmpPath, partDir, lb)
	case ext == ".zip":
		err = extractZipFlat(tmpPath, partDir)
	default:
		return fmt.Errorf("unsupported archive format: %s", ext)
	}
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("remove old target: %w", err)
	}
	if err := os.Rename(partDir, targetDir); err != nil {
		return fmt.Errorf("move to target: %w", err)
	}

	bin := filepath.Join(targetDir, "bin", "ollama")
	switch goos {
	case "windows":
		bin = filepath.Join(targetDir, "ollama.exe")
	case "darwin":
		bin = filepath.Join(targetDir, "ollama")
	}
	if err := os.Chmod(bin, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	inst.setProgress("ollama", "done")
	lb.Write("Ollama installed successfully")
	log.Printf("[ollama] installed to %s", targetDir)
	return nil
}

func extractOllamaTarZst(archivePath, targetDir string, lb *process.RingBuffer) error {
	cmd := hide.Command("tar", "-I", "zstd", "-xf", archivePath, "-C", targetDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		cmd = hide.Command("tar", "--zstd", "-xf", archivePath, "-C", targetDir)
		if output2, err2 := cmd.CombinedOutput(); err2 != nil {
			return fmt.Errorf("tar extract (tried -I zstd and --zstd): %s; %s: %w", strings.TrimSpace(string(output)), strings.TrimSpace(string(output2)), err2)
		}
	}
	return nil
}

func extractOllamaTgz(archivePath, targetDir string, lb *process.RingBuffer) error {
	out, err := hide.Command("tar", "-xzf", archivePath, "-C", targetDir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar extract: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
