package installer

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"imagecaster-server/hide"
	"imagecaster-server/process"
)

const NeoPythonVersion = "3.13"

func VenvPython(workDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(workDir, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(workDir, "venv", "bin", "python")
}

func (inst *Installer) setupBackend(target string, lb *process.RingBuffer) error {
	if _, err := os.Stat(VenvPython(target)); err == nil {
		lb.Write("venv already exists")
		return nil
	}
	if err := inst.EnsureGit(); err != nil {
		lb.Write("git: " + err.Error())
		log.Printf("[backend] git unavailable, continuing: %v", err)
	}
	lb.Write("Installing uv " + UvVersion + "...")
	uv, err := EnsureUv(inst.config.DataDir)
	if err != nil {
		return fmt.Errorf("uv: %w", err)
	}
	lb.Write("Creating venv with Python " + NeoPythonVersion + " (uv downloads Python)...")
	cmd := exec.CommandContext(inst.ctx, uv, "venv", "venv", "--python", NeoPythonVersion, "--managed-python", "--seed", "--clear")
	hide.Window(cmd)
	cmd.Dir = target
	cmd.Env = UvEnv(inst.config.DataDir)
	cmd.WaitDelay = 5 * time.Second
	inst.running.Lock()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if rmErr := os.RemoveAll(filepath.Join(target, "venv")); rmErr != nil {
			log.Printf("[backend] remove unfinished venv: %v", rmErr)
		}
	}
	inst.running.Unlock()
	if err != nil {
		lb.Write(string(out))
		return fmt.Errorf("uv venv: %w", err)
	}
	log.Printf("[backend] venv created in %s", target)
	lb.Write("venv created; PyTorch and requirements are installed by Forge Neo on its first start")
	return nil
}
