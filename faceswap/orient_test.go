package faceswap

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyOrientation(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	tests := []struct {
		o          int
		w, h, x, y int
	}{
		{1, 3, 2, 0, 0},
		{2, 3, 2, 2, 0},
		{3, 3, 2, 2, 1},
		{4, 3, 2, 0, 1},
		{5, 2, 3, 0, 0},
		{6, 2, 3, 1, 0},
		{7, 2, 3, 1, 2},
		{8, 2, 3, 0, 2},
	}
	for _, tt := range tests {
		out := applyOrientation(src, tt.o)
		b := out.Bounds()
		assert.Equal(t, [2]int{tt.w, tt.h}, [2]int{b.Dx(), b.Dy()}, "orientation %d size", tt.o)
		r, _, _, _ := out.At(tt.x, tt.y).RGBA()
		assert.Equal(t, uint32(0xffff), r, "orientation %d: red corner must land at %d,%d", tt.o, tt.x, tt.y)
	}
}

func exifJPEG(t *testing.T, orientation uint16, bo binary.ByteOrder) []byte {
	var plain bytes.Buffer
	require.NoError(t, jpeg.Encode(&plain, image.NewGray(image.Rect(0, 0, 4, 2)), nil))
	tiff := new(bytes.Buffer)
	if bo == binary.LittleEndian {
		tiff.WriteString("II")
	} else {
		tiff.WriteString("MM")
	}
	_ = binary.Write(tiff, bo, uint16(42))
	_ = binary.Write(tiff, bo, uint32(8))
	_ = binary.Write(tiff, bo, uint16(1))
	_ = binary.Write(tiff, bo, uint16(0x0112))
	_ = binary.Write(tiff, bo, uint16(3))
	_ = binary.Write(tiff, bo, uint32(1))
	_ = binary.Write(tiff, bo, orientation)
	_ = binary.Write(tiff, bo, uint16(0))
	_ = binary.Write(tiff, bo, uint32(0))
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	b := plain.Bytes()
	out := append([]byte{}, b[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, b[2:]...)
}

func TestJPEGOrientation(t *testing.T) {
	assert.Equal(t, 6, jpegOrientation(bytes.NewReader(exifJPEG(t, 6, binary.LittleEndian))))
	assert.Equal(t, 8, jpegOrientation(bytes.NewReader(exifJPEG(t, 8, binary.BigEndian))))
	assert.Equal(t, 1, jpegOrientation(bytes.NewReader(exifJPEG(t, 42, binary.BigEndian))))
	assert.Equal(t, 1, jpegOrientation(bytes.NewReader([]byte("\x89PNG\r\n"))))

	p := filepath.Join(t.TempDir(), "r.jpg")
	require.NoError(t, os.WriteFile(p, exifJPEG(t, 6, binary.LittleEndian), 0o600))
	img, err := loadImage(p)
	require.NoError(t, err)
	assert.Equal(t, [2]int{2, 4}, [2]int{img.Bounds().Dx(), img.Bounds().Dy()}, "phone photo rotated by EXIF before face detection")
}

func TestLoadImageRejectsHugeDimensions(t *testing.T) {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 30000)
	binary.BigEndian.PutUint32(ihdr[4:], 30000)
	ihdr[8], ihdr[9] = 8, 2
	chunk := new(bytes.Buffer)
	_ = binary.Write(chunk, binary.BigEndian, uint32(len(ihdr)))
	body := append([]byte("IHDR"), ihdr...)
	chunk.Write(body)
	_ = binary.Write(chunk, binary.BigEndian, crc32.ChecksumIEEE(body))
	p := filepath.Join(t.TempDir(), "bomb.png")
	require.NoError(t, os.WriteFile(p, append([]byte("\x89PNG\r\n\x1a\n"), chunk.Bytes()...), 0o600))
	_, err := loadImage(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

func TestJPEGOrientationBrokenInput(t *testing.T) {
	good := exifJPEG(t, 6, binary.LittleEndian)
	app1 := []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x14}
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"soi only", []byte{0xFF, 0xD8}},
		{"segment cut short", good[:10]},
		{"app1 header without body", app1},
		{"ifd offset past end", append(append(append([]byte{}, app1...), "Exif\x00\x00II*\x00"...), 0xFF, 0xFF, 0x00, 0x00, 0, 0, 0, 0, 0, 0)},
		{"entry count past end", append(append(append([]byte{}, app1...), "Exif\x00\x00II*\x00"...), 8, 0, 0, 0, 0xFF, 0x7F, 0, 0, 0, 0)},
		{"bad byte order", append(append(append([]byte{}, app1...), "Exif\x00\x00XX*\x00"...), 8, 0, 0, 0, 0, 0, 0, 0, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				assert.Equal(t, 1, jpegOrientation(bytes.NewReader(tt.data)))
			})
		})
	}
}
