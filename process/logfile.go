package process

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

const (
	MaxLogFileSize = 20 << 20
	maxLogLine     = 1 << 20
)

type RotatingFile struct {
	mu     sync.Mutex
	path   string
	max    int64
	f      *os.File
	size   int64
	closed bool
}

func OpenRotatingFile(path string, maxSize int64) (*RotatingFile, error) {
	r := &RotatingFile{path: path, max: maxSize}
	if err := r.open(); err != nil {
		return nil, err
	}
	if r.size > r.max {
		if err := r.rotate(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "log rotate %s: %v\n", r.path, err)
	}
	r.f = nil
	rotated := true
	backup := r.path + ".1"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "log rotate %s: %v\n", backup, err)
	}
	if err := os.Rename(r.path, backup); err != nil {
		fmt.Fprintf(os.Stderr, "log rotate %s: %v\n", r.path, err)
		rotated = false
	}
	if err := r.open(); err != nil {
		return err
	}
	if !rotated {
		r.size = 0
	}
	return nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func pipeLogs(r io.Reader, buf *RingBuffer, logFile *RotatingFile, name string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxLogLine)
	for scanner.Scan() {
		line := scanner.Text()
		buf.Write(line)
		log.Printf("[%s] %s", name, line)
		if logFile != nil {
			if _, err := logFile.Write([]byte(time.Now().Format("2006-01-02 15:04:05") + " " + line + "\n")); err != nil && !errors.Is(err, os.ErrClosed) {
				log.Printf("[%s] log file: %v", name, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		msg := "output not logged any more: " + err.Error()
		buf.Write(msg)
		log.Printf("[%s] %s", name, msg)
		if _, err := io.Copy(io.Discard, r); err != nil {
			log.Printf("[%s] drain output: %v", name, err)
		}
	}
}
