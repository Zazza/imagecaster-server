package imgb64

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encode(t *testing.T, w, h int, jpg bool) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if jpg {
		require.NoError(t, jpeg.Encode(&buf, img, nil))
	} else {
		require.NoError(t, png.Encode(&buf, img))
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func headerOnlyPNG(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1))))
	b := buf.Bytes()
	ihdr := b[:33]
	ihdr[16], ihdr[17], ihdr[18], ihdr[19] = byte(w>>24), byte(w>>16), byte(w>>8), byte(w)
	ihdr[20], ihdr[21], ihdr[22], ihdr[23] = byte(h>>24), byte(h>>16), byte(h>>8), byte(h)
	binary.BigEndian.PutUint32(ihdr[29:33], crc32.ChecksumIEEE(ihdr[12:29]))
	return base64.StdEncoding.EncodeToString(ihdr)
}

var lim = Limits{MaxSide: 100, MaxPixels: 5000}

func TestCheckAndDecode(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantW     int
		wantH     int
		checkErr  bool
		decodeErr bool
	}{
		{name: "png", in: encode(t, 20, 10, false), wantW: 20, wantH: 10},
		{name: "jpeg", in: encode(t, 16, 8, true), wantW: 16, wantH: 8},
		{name: "data url", in: "data:image/png;base64," + encode(t, 5, 6, false), wantW: 5, wantH: 6},
		{name: "side over limit", in: encode(t, 101, 1, false), checkErr: true, decodeErr: true},
		{name: "pixels over limit", in: encode(t, 80, 80, false), checkErr: true, decodeErr: true},
		{name: "not base64", in: "!!!", checkErr: true, decodeErr: true},
		{name: "broken base64 tail", in: encode(t, 4, 4, false) + "!!", checkErr: true, decodeErr: true},
		{name: "not an image", in: base64.StdEncoding.EncodeToString([]byte("hello world")), checkErr: true, decodeErr: true},
		{name: "header is enough for check", in: headerOnlyPNG(t, 50, 50), wantW: 50, wantH: 50, decodeErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h, err := Check(tc.in, lim)
			if tc.checkErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, [2]int{tc.wantW, tc.wantH}, [2]int{w, h})
			}
			img, err := Decode(tc.in, lim)
			if tc.decodeErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, image.Rect(0, 0, tc.wantW, tc.wantH), img.Bounds())
		})
	}
}

func TestSizeIgnoresLimits(t *testing.T) {
	w, h, err := Size(encode(t, 300, 2, false))
	require.NoError(t, err)
	assert.Equal(t, 300, w)
	assert.Equal(t, 2, h)
	_, _, err = Size("@@")
	assert.Error(t, err)
}

func TestLimitsAllowed(t *testing.T) {
	cases := []struct {
		w, h int
		want bool
	}{
		{1, 1, true}, {100, 50, true}, {0, 5, false}, {5, -1, false}, {101, 1, false}, {100, 51, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, lim.Allowed(tc.w, tc.h), "%dx%d", tc.w, tc.h)
	}
}
