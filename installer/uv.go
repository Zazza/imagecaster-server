package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const UvVersion = "0.12.19"

type uvAsset struct {
	url    string
	sha256 string
}

const uvBase = "https://github.com/astral-sh/uv/releases/download/" + UvVersion + "/uv-"

var uvAssets = map[string]uvAsset{
	"windows/amd64": {uvBase + "x86_64-pc-windows-msvc.zip", "6dbb02d79e419522f1c500f0adb1cddcff0cda7d59b0d66ea7f5e3b4a1b2f5f0"},
	"windows/arm64": {uvBase + "aarch64-pc-windows-msvc.zip", "115b54cb823bc48260670f5782001add6067ac8d98d18c8263a833704e287de9"},
	"linux/amd64":   {uvBase + "x86_64-unknown-linux-gnu.tar.gz", "23bf5552d220e0842b65c862097b2ebaeba0064b74eda5e565e77fd25969d8c8"},
	"linux/arm64":   {uvBase + "aarch64-unknown-linux-gnu.tar.gz", "0804e9b164c64b6914182d5920c08551958a095986f10a3731056df701126436"},
	"darwin/amd64":  {uvBase + "x86_64-apple-darwin.tar.gz", "cb5fa57bafe68fc0fb94b17f06bee0b0b9a7feb94ccbd110445afa0696e39273"},
	"darwin/arm64":  {uvBase + "aarch64-apple-darwin.tar.gz", "a9a8df1eedeb192f2e47e40e2faabfb387db4b850209118786d42f89dde3e0ba"},
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func UvPath(dataDir string) string {
	return filepath.Join(dataDir, "bin", exeName("uv"))
}

func UvEnv(dataDir string) []string {
	return append(os.Environ(),
		"UV_PYTHON_INSTALL_DIR="+filepath.Join(dataDir, "python"),
		"UV_CACHE_DIR="+filepath.Join(dataDir, "uv-cache"),
		"UV_SYSTEM_CERTS=1",
	)
}

func EnsureUv(dataDir string) (string, error) {
	uv := UvPath(dataDir)
	if _, err := os.Stat(uv); err == nil {
		return uv, nil
	}
	a, ok := uvAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no uv build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return installUv(a, uv)
}

func installUv(a uvAsset, uv string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(uv), 0o755); err != nil {
		return "", err
	}
	tmp := uv + ".download"
	defer os.Remove(tmp)
	if err := downloadFile(a.url, tmp); err != nil {
		return "", err
	}
	if err := verifySHA256(tmp, a.sha256); err != nil {
		return "", err
	}
	if err := extractUv(tmp, strings.HasSuffix(a.url, ".zip"), uv+".part"); err != nil {
		os.Remove(uv + ".part")
		return "", err
	}
	return uv, os.Rename(uv+".part", uv)
}

func downloadFile(url, dst string) error {
	resp, err := HTTPGet(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("sha256 mismatch for %s: got %s, want %s", filepath.Base(path), got, want)
	}
	return nil
}

func extractUv(archive string, isZip bool, dst string) error {
	want := exeName("uv")
	if isZip {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if filepath.Base(f.Name) != want {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return writeExecutable(dst, rc)
		}
		return fmt.Errorf("%s not found in uv archive", want)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s not found in uv archive", want)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == want {
			return writeExecutable(dst, tr)
		}
	}
}

func writeExecutable(dst string, r io.Reader) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
