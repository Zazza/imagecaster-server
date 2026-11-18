package faceswap

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	IdentityModel    = "w600k_r50"
	MinIdentityFaces = 3
	maxIdentitySide  = 2048
	maxDecodeSide    = 8192
	maxDecodePixels  = 40_000_000
)

var (
	ErrPersonNotFound = errors.New("person not found")
	ErrNoIdentity     = errors.New("person has no face identity")
	ErrTooFewFaces    = errors.New("too few photos with a usable face")
)

type Identity struct {
	Model      string    `json:"model"`
	Embedding  []float32 `json:"embedding"`
	PhotosUsed int       `json:"photos_used"`
	CreatedAt  time.Time `json:"created_at"`
}

func meanIdentity(embs [][]float32) (Identity, error) {
	if len(embs) < MinIdentityFaces {
		return Identity{}, fmt.Errorf("%w: %d of %d needed", ErrTooFewFaces, len(embs), MinIdentityFaces)
	}
	sum := make([]float32, embDim)
	for _, e := range embs {
		for i, v := range normalize(e) {
			sum[i] += v / float32(len(embs))
		}
	}
	return Identity{Model: IdentityModel, Embedding: normalize(sum), PhotosUsed: len(embs), CreatedAt: time.Now().UTC()}, nil
}

func (e *Engine) FromImages(ctx context.Context, paths []string) (Identity, error) {
	var embs [][]float32
	for _, p := range paths {
		img, err := loadImage(p)
		if err != nil {
			log.Printf("faceswap: identity photo %s skipped: %v", filepath.Base(p), err)
			continue
		}
		emb, err := e.faceEmbedding(ctx, img)
		if err != nil {
			if errors.Is(err, ErrUnavailable) || ctx.Err() != nil {
				return Identity{}, err
			}
			log.Printf("faceswap: identity photo %s skipped: %v", filepath.Base(p), err)
			continue
		}
		if emb != nil {
			embs = append(embs, emb)
		}
	}
	return meanIdentity(embs)
}

func (e *Engine) faceEmbedding(ctx context.Context, img image.Image) ([]float32, error) {
	unlock := e.lock()
	defer unlock()
	f, ok, err := e.largestFace(ctx, fromImage(img), false, true)
	if err != nil || !ok || f.height() < MinFacePx {
		return nil, err
	}
	return f.emb, nil
}

func loadImage(path string) (image.Image, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return nil, err
	}
	if max(cfg.Width, cfg.Height) > maxDecodeSide || cfg.Width*cfg.Height > maxDecodePixels {
		return nil, fmt.Errorf("image %dx%d is too large", cfg.Width, cfg.Height)
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(fh)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if side := max(b.Dx(), b.Dy()); side > maxIdentitySide {
		w, h := b.Dx()*maxIdentitySide/side, b.Dy()*maxIdentitySide/side
		dst := image.NewNRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
		xdraw.BiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
		img = dst
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return applyOrientation(img, jpegOrientation(fh)), nil
}
