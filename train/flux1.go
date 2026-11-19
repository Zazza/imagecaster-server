package train

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed launcher/flux1
var flux1Files embed.FS

const flux1Root = "launcher/flux1"

func writeFlux1Config(workDir string) (string, error) {
	dst, err := filepath.Abs(filepath.Join(workDir, "flux1-config"))
	if err != nil {
		return "", err
	}
	err = fs.WalkDir(flux1Files, flux1Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(flux1Root, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := flux1Files.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
	return dst, err
}
