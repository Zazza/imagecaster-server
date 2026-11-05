//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"imagecaster-server/hide"
	"imagecaster-server/i18n"
)

const idYes = 6

var ErrBusy = errors.New("folder is used by another Imagecaster Server")

func messageBox(text string, flags uint32) int32 {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Imagecaster Server")
	ret, _ := windows.MessageBox(0, t, c, flags)
	return ret
}

func ShowError(text string) { messageBox(text, windows.MB_OK|windows.MB_ICONERROR) }
func ShowInfo(text string)  { messageBox(text, windows.MB_OK|windows.MB_ICONINFORMATION) }

func askYesNo(text string) bool {
	return messageBox(text, windows.MB_YESNO|windows.MB_ICONQUESTION|windows.MB_DEFBUTTON2) == idYes
}

const createBreakawayFromJob = 0x01000000

func startOutsideJob(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createBreakawayFromJob}
	_ = cmd.Start()
}

func openFile(path string) {
	startOutsideJob("notepad.exe", path)
}

func openURL(url string) {
	startOutsideJob("rundll32", "url.dll,FileProtocolHandler", url)
}

func copyText(text string) {
	cmd := hide.Command("clip")
	cmd.Stdin = strings.NewReader(text)
	_ = cmd.Run()
}

func openConsole(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
	return cmd.Start()
}

func BindSelfToJob() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	return windows.AssignProcessToJobObject(job, windows.CurrentProcess())
}

func LockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "imagecaster-server.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{})
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock %s: %w", dir, err)
	}
	return f, nil
}

func freeGB(dir string) float64 {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if p, err := windows.UTF16PtrFromString(d); err == nil {
			var free uint64
			if windows.GetDiskFreeSpaceEx(p, &free, nil, nil) == nil {
				return float64(free) / (1 << 30)
			}
		}
		if filepath.Dir(d) == d {
			return -1
		}
	}
}

type gpuInfo struct {
	Name   string
	VRAMGB int
}

func detectGPU() (gpuInfo, error) {
	out, err := hide.Command("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return gpuInfo{}, errors.New(i18n.T("desktop.no_gpu"))
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	parts := strings.Split(line, ",")
	if len(parts) < 2 {
		return gpuInfo{}, errors.New(i18n.T("desktop.no_gpu"))
	}
	mib, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	return gpuInfo{Name: strings.TrimSpace(parts[0]), VRAMGB: (mib + 512) / 1024}, nil
}
