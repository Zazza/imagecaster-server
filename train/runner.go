package train

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"imagecaster-server/process"
)

//go:embed launcher/aitk_local.py
var launcherScript []byte

const launcherName = "aitk_local.py"

var progressRe = regexp.MustCompile(`(\d+)/(\d+) \[`)

type processRunner struct {
	python     string
	toolkitDir string
}

func NewProcessRunner(python, toolkitDir string) Runner {
	return &processRunner{python: python, toolkitDir: toolkitDir}
}

func (p *processRunner) Run(ctx context.Context, spec RunSpec, progress func(step, steps int)) error {
	launcher := filepath.Join(spec.WorkDir, launcherName)
	if err := os.WriteFile(launcher, launcherScript, 0o644); err != nil {
		return fmt.Errorf("write trainer launcher: %w", err)
	}
	cmd := exec.CommandContext(ctx, p.python, launcher, p.toolkitDir, spec.ConfigPath, spec.LocalPath)
	cmd.Dir = p.toolkitDir
	cmd.Env = append(os.Environ(), "PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True", "PYTHONUNBUFFERED=1")
	process.SetPlatformProcAttr(cmd)
	cmd.Cancel = func() error { return process.PlatformKill(cmd.Process.Pid, nil) }
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	var logOut io.Writer = io.Discard
	if spec.LogPath != "" {
		if f, err := os.Create(spec.LogPath); err == nil {
			defer f.Close()
			logOut = f
		} else {
			log.Printf("[train] create log %s: %v", spec.LogPath, err)
		}
	}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return fmt.Errorf("start trainer: %w", err)
	}
	pw.Close()
	tail := &tailBuffer{max: 4096}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanProgress(io.TeeReader(pr, io.MultiWriter(logOut, tail)), spec.Steps, progress)
	}()
	waitErr := cmd.Wait()
	wg.Wait()
	pr.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("trainer failed: %w: %s", waitErr, lastLine(tail.String()))
	}
	return nil
}

func scanProgress(r io.Reader, steps int, progress func(step, steps int)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(splitCRLF)
	for sc.Scan() {
		m := progressRe.FindAllStringSubmatch(sc.Text(), -1)
		if len(m) == 0 {
			continue
		}
		last := m[len(m)-1]
		step, err1 := strconv.Atoi(last[1])
		total, err2 := strconv.Atoi(last[2])
		if err1 != nil || err2 != nil || total <= 0 || (steps > 0 && total != steps) {
			continue
		}
		progress(step, total)
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		log.Printf("[train] drain trainer output: %v", err)
	}
}

func splitCRLF(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func lastLine(s string) string {
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
