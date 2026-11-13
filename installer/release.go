package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"imagecaster-server/config"
	"imagecaster-server/hide"
	"imagecaster-server/process"
)

type GPUKind int

const (
	GPUNone GPUKind = iota
	GPUNvidia
	GPUVulkan
)

var ErrUnsupportedPlatform = errors.New("no stable-diffusion.cpp build for this platform")

type Artifact struct {
	Name   string
	URL    string
	SHA256 string
	SHAURL string
	Size   int64
	Kind   string
	Dest   string
	Strip  int
}

const (
	sdcppBase   = "https://github.com/leejet/stable-diffusion.cpp/releases/download/" + config.SDCppVersion + "/"
	ownBase     = "https://github.com/Zazza/imagecaster-server/releases/download/sdcpp-465adf3/"
	onnxBase    = "https://github.com/microsoft/onnxruntime/releases/download/v" + config.OnnxVersion + "/"
	dwposeBase  = "https://huggingface.co/yzd-v/DWPose/resolve/main/"
	onnxTarget  = "engines/onnxruntime"
	poseTarget  = "models/openpose"
	installMark = ".installed"
)

// sdcppTarget — каталог движка с суффиксом целевой платформы
// (общая папка данных у Windows и Linux).
func sdcppTarget(goos string) string {
	return config.EngineDirFor(config.SDCppDirLegacy, goos)
}

func sdcppZip(goos, name, sha string, size int64) Artifact {
	return Artifact{Name: name, URL: sdcppBase + name, SHA256: sha, Size: size, Kind: "zip", Dest: sdcppTarget(goos)}
}

func ownBuild(goos, name string) Artifact {
	return Artifact{Name: name, URL: ownBase + name, SHAURL: ownBase + name + ".sha256", Kind: "tgz", Dest: sdcppTarget(goos)}
}

func onnxArtifact(goos string) Artifact {
	a := Artifact{Kind: "tgz", Dest: onnxTarget, Strip: 1}
	switch goos {
	case "windows":
		a.Name, a.SHA256, a.Size, a.Kind = "onnxruntime-win-x64-1.31.1.zip", "5869e26bb25314375f2d5ef1877e3ea06b1543d6c710e6e4a1ec2f7177ab4b2c", 82929944, "zip"
	case "darwin":
		a.Name, a.SHA256, a.Size = "onnxruntime-osx-arm64-1.31.1.tgz", "2e1344a1926ee50eb25b38153cb6992c3567515aef0e677a1a520a1798896d6f", 36936435
	default:
		a.Name, a.SHA256, a.Size = "onnxruntime-linux-x64-1.31.1.tgz", "c849e36a1ef3e22d4ff217b9d1ce4a3cf5b9467b975610913ae959ad5b16bdc9", 11413655
	}
	a.URL = onnxBase + a.Name
	return a
}

func poseArtifacts() []Artifact {
	return []Artifact{
		{Name: "yolox_l.onnx", URL: dwposeBase + "yolox_l.onnx", SHA256: "7860ae79de6c89a3c1eb72ae9a2756c0ccfbe04b7791bb5880afabd97855a411", Size: 216746733, Kind: "file", Dest: poseTarget + "/yolox_l.onnx"},
		{Name: "dw-ll_ucoco_384.onnx", URL: dwposeBase + "dw-ll_ucoco_384.onnx", SHA256: "724f4ff2439ed61afb86fb8a1951ec39c6220682803b4a8bd4f598cd913b1843", Size: 134399116, Kind: "file", Dest: poseTarget + "/dw-ll_ucoco_384.onnx"},
	}
}

func SDCppArtifacts(goos, goarch string, gpu GPUKind) ([]Artifact, error) {
	var engine []Artifact
	switch {
	case goos == "windows" && goarch == "amd64" && gpu == GPUNvidia:
		engine = []Artifact{
			sdcppZip(goos, "sd-master-465adf3-bin-win-cuda12-x64.zip", "d775bff22f7677af664c198d0d978098990bf45525aa538bb7f630f8329ea5ad", 367151447),
			sdcppZip(goos, "cudart-sd-bin-win-cu12-x64.zip", "fe20366827d357c00797eebb58244dddab7fd9a348d70090c3871004c320f38d", 563452046),
		}
	case goos == "windows" && goarch == "amd64" && gpu == GPUVulkan:
		engine = []Artifact{sdcppZip(goos, "sd-master-465adf3-bin-win-vulkan-x64.zip", "f468c6c4c1130e7d7facafe2163139e259739f480ef2413d968057e1e691c817", 30023026)}
	case goos == "windows" && goarch == "amd64":
		engine = []Artifact{sdcppZip(goos, "sd-master-465adf3-bin-win-cpu-x64.zip", "6988bec67b291b2ad293cb1b69c71fb8793ae107a1c6a14699ab5570e1a7edef", 18256679)}
	case goos == "darwin" && goarch == "arm64":
		engine = []Artifact{sdcppZip(goos, "sd-master-465adf3-bin-Darwin-macOS-26.7.2-arm64.zip", "f04c897e127fa1f0afa5cf5a0e182804c84c796df5008f5f2706339afa0ed0c9", 38089008)}
	case goos == "linux" && goarch == "amd64" && gpu == GPUNvidia:
		engine = []Artifact{ownBuild(goos, "sdcpp-linux-x86_64-cuda.tar.gz")}
	case goos == "linux" && goarch == "amd64" && gpu == GPUVulkan:
		engine = []Artifact{ownBuild(goos, "sdcpp-linux-x86_64-vulkan.tar.gz")}
	case goos == "linux" && goarch == "amd64":
		engine = []Artifact{ownBuild(goos, "sdcpp-linux-x86_64-cpu.tar.gz")}
	default:
		return nil, fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, goos, goarch)
	}
	return append(append(engine, onnxArtifact(goos)), poseArtifacts()...), nil
}

func detectGPUKind() GPUKind {
	bin := "nvidia-smi"
	if runtime.GOOS == "windows" {
		if p, err := findNvidiaSMIWindows(); err == nil {
			bin = p
		}
	}
	if out, err := hide.Command(bin, "-L").Output(); err == nil && strings.Contains(string(out), "GPU ") {
		return GPUNvidia
	}
	if vulkanUsable(runtime.GOOS) {
		return GPUVulkan
	}
	return GPUNone
}

func vulkanUsable(goos string) bool {
	switch goos {
	case "windows":
		root := os.Getenv("SystemRoot")
		return root != "" && fileExists(filepath.Join(root, "System32", "vulkan-1.dll"))
	case "linux":
		nodes, _ := filepath.Glob("/dev/dri/renderD*")
		if len(nodes) == 0 {
			return false
		}
		for _, p := range []string{"/usr/lib/x86_64-linux-gnu/libvulkan.so.1", "/usr/lib64/libvulkan.so.1", "/usr/lib/libvulkan.so.1"} {
			if fileExists(p) {
				return true
			}
		}
	}
	return false
}

func findNvidiaSMIWindows() (string, error) {
	for _, env := range []string{"ProgramW6432", "ProgramFiles"} {
		if base := os.Getenv(env); base != "" {
			p := filepath.Join(base, "NVIDIA Corporation", "NVSMI", "nvidia-smi.exe")
			if fileExists(p) {
				return p, nil
			}
		}
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "nvidia-smi.exe")
		if fileExists(p) {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}

func (inst *Installer) releaseArtifacts() ([]Artifact, error) {
	if inst.ReleaseArtifacts != nil {
		return inst.ReleaseArtifacts()
	}
	return SDCppArtifacts(runtime.GOOS, runtime.GOARCH, detectGPUKind())
}

func (inst *Installer) releaseInstalled(ic config.InstallConfig) bool {
	b, err := os.ReadFile(filepath.Join(inst.resolveTarget(ic.Target), installMark))
	return err == nil && strings.TrimSpace(string(b)) == ic.Version
}

func (inst *Installer) artifactDest(key string, a Artifact) string {
	if a.Kind == "file" && strings.HasPrefix(filepath.ToSlash(a.Dest), poseTarget+"/") {
		if b, ok := inst.config.Backends[key]; ok && b.PoseModelsDir != "" {
			return filepath.Join(inst.resolveTarget(b.PoseModelsDir), filepath.Base(a.Dest))
		}
	}
	return inst.resolveTarget(a.Dest)
}

func (inst *Installer) installRelease(key string, ic config.InstallConfig, lb *process.RingBuffer) error {
	arts, err := inst.artifactsFor(key)
	if err != nil {
		return err
	}
	target := inst.resolveTarget(ic.Target)
	if err := os.Remove(filepath.Join(target, installMark)); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, a := range arts {
		if err := inst.fetchArtifact(key, a, inst.artifactDest(key, a), ic.Version, lb); err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"sd-server", "sd-cli", "llama-server"} {
			if p := filepath.Join(target, name); fileExists(p) {
				if err := os.Chmod(p, 0o755); err != nil {
					return err
				}
			}
		}
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, installMark), []byte(ic.Version+"\n"), 0o644); err != nil {
		return err
	}
	lb.Write("Installation complete")
	log.Printf("[%s] installed %s to %s", key, ic.Version, target)
	return nil
}

func (inst *Installer) fetchArtifact(key string, a Artifact, dest, version string, lb *process.RingBuffer) error {
	want := a.SHA256
	if want == "" && a.SHAURL != "" {
		s, err := fetchSHA(a.SHAURL)
		if err != nil {
			return fmt.Errorf("checksum: %w", err)
		}
		want = s
	}
	if len(want) != 64 {
		return fmt.Errorf("no sha256 for %s", a.Name)
	}
	if a.Kind == "file" && fileExists(dest) && verifySHA256(dest, want) == nil {
		lb.Write(a.Name + " already present")
		return nil
	}
	stamp := filepath.Join(dest, ".artifact-"+safeName(a.Name))
	if a.Kind != "file" {
		if got, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(got)) == version+" "+strings.ToLower(want) {
			lb.Write(a.Name + " already extracted")
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+"."+safeName(a.Name)+".part")
	if err := inst.downloadResumable(key, a, part, want, lb); err != nil {
		return err
	}
	defer os.Remove(part)
	tmpPath := part
	switch a.Kind {
	case "file":
		return renameRetry(tmpPath, dest)
	case "zip", "tgz":
		inst.setProgress(key, "extracting")
		if err := extractArchive(tmpPath, a.Kind, dest, a.Strip); err != nil {
			return err
		}
		return os.WriteFile(stamp, []byte(version+" "+strings.ToLower(want)+"\n"), 0o644)
	}
	return fmt.Errorf("unknown artifact kind %q", a.Kind)
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, s)
}

func (inst *Installer) downloadResumable(key string, a Artifact, part, want string, lb *process.RingBuffer) error {
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
	}
	if have > 0 && a.Size > 0 && have >= a.Size {
		if verifySHA256(part, want) == nil {
			lb.Write(a.Name + " already downloaded")
			return nil
		}
		if err := os.Remove(part); err != nil {
			return err
		}
		have = 0
	}
	inst.setProgress(key, "downloading")
	lb.Write(fmt.Sprintf("Downloading %s", a.URL))
	resp, err := HTTPGetFrom(a.URL, have)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	switch {
	case have > 0 && resp.StatusCode == http.StatusPartialContent:
		flags = os.O_WRONLY | os.O_APPEND
		lb.Write(fmt.Sprintf("Resuming %s from %s", a.Name, FormatBytes(have)))
	case resp.StatusCode == http.StatusOK:
		have = 0
	case have > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		if verifySHA256(part, want) == nil {
			lb.Write(a.Name + " already downloaded")
			return nil
		}
		if err := os.Remove(part); err != nil {
			return err
		}
		return fmt.Errorf("download failed: HTTP %d, partial file discarded", resp.StatusCode)
	default:
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	total := a.Size
	if resp.ContentLength > 0 {
		total = have + resp.ContentLength
	}
	pw := &progressWriter{w: f, total: total, written: have, key: key, target: a.Name, inst: inst, lb: lb}
	_, copyErr := io.Copy(pw, resp.Body)
	if err := f.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		return fmt.Errorf("save download: %w", copyErr)
	}
	if err := verifySHA256(part, want); err != nil {
		if rmErr := os.Remove(part); rmErr != nil {
			log.Printf("[%s] remove bad download %s: %v", key, part, rmErr)
		}
		return err
	}
	return nil
}

func fetchSHA(url string) (string, error) {
	resp, err := HTTPGet(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "", errors.New("empty checksum file")
	}
	return f[0], nil
}

func stripPath(name string, strip int) (string, bool) {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	parts := strings.Split(strings.Trim(name, "/"), "/")
	if len(parts) <= strip || parts[0] == "" {
		return "", false
	}
	return strings.Join(parts[strip:], "/"), true
}

func extractArchive(archive, kind, dest string, strip int) error {
	parent, base := filepath.Dir(dest), filepath.Base(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(parent, "."+base+".part-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if kind == "zip" {
		err = extractZipStrip(archive, tmpDir, strip)
	} else {
		err = extractTgzStrip(archive, tmpDir, strip)
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmpDir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return renameRetry(tmpDir, dest)
	}
	return mergeInto(tmpDir, dest)
}

func extractZipStrip(archive, root string, strip int) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name, ok := stripPath(f.Name, strip)
		if !ok {
			continue
		}
		if err := writeZipEntry(root, name, f); err != nil {
			return err
		}
	}
	return nil
}

func extractTgzStrip(archive, root string, strip int) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, ok := stripPath(hdr.Name, strip)
		if !ok {
			continue
		}
		dest, err := safeJoin(root, name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeTarFile(dest, tr, hdr.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := writeTarSymlink(root, dest, hdr.Linkname); err != nil {
				return err
			}
		}
	}
}

func writeTarSymlink(root, dest, link string) error {
	if filepath.IsAbs(link) || strings.Contains(filepath.ToSlash(link), "..") {
		return fmt.Errorf("tar symlink escapes target: %s -> %s", dest, link)
	}
	if _, err := safeJoin(root, filepath.Join(filepath.Dir(strings.TrimPrefix(dest, root)), link)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(link, dest)
}

func writeTarFile(dest string, r io.Reader, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
