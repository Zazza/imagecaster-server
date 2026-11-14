package imgb64

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"io"
	"strings"

	_ "image/jpeg"
	_ "image/png"
)

type Limits struct {
	MaxSide   int
	MaxPixels int64
}

func (l Limits) Allowed(w, h int) bool {
	return w > 0 && h > 0 && w <= l.MaxSide && h <= l.MaxSide && int64(w)*int64(h) <= l.MaxPixels
}

func (l Limits) check(w, h int) error {
	if !l.Allowed(w, h) {
		return fmt.Errorf("image %dx%d exceeds %d px per side or %d MP", w, h, l.MaxSide, l.MaxPixels/1_000_000)
	}
	return nil
}

func payload(s string) string {
	if i := strings.Index(s, ","); i >= 0 && strings.HasPrefix(s, "data:") {
		return s[i+1:]
	}
	return s
}

func reader(s string) *bufio.Reader {
	return bufio.NewReader(base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload(s))))
}

func Size(s string) (int, int, error) {
	cfg, _, err := image.DecodeConfig(reader(s))
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

func Check(s string, lim Limits) (int, int, error) {
	br := reader(s)
	cfg, _, err := image.DecodeConfig(br)
	if err != nil {
		return 0, 0, err
	}
	if err := lim.check(cfg.Width, cfg.Height); err != nil {
		return 0, 0, err
	}
	if _, err := io.Copy(io.Discard, br); err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

func Decode(s string, lim Limits) (image.Image, error) {
	b, err := base64.StdEncoding.DecodeString(payload(s))
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if err := lim.check(cfg.Width, cfg.Height); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}
