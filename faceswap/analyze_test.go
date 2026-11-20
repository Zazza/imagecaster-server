package faceswap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFaceSharpness_FlatVsDetailed(t *testing.T) {
	flat := newPlane(200, 200, 3)
	detailed := newPlane(200, 200, 3)
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			v := float32(0)
			if (x/2+y/2)%2 == 0 {
				v = 255
			}
			for c := 0; c < 3; c++ {
				flat.pix[(y*200+x)*3+c] = 128
				detailed.pix[(y*200+x)*3+c] = v
			}
		}
	}
	box := [4]float64{20, 20, 180, 180}
	assert.Zero(t, faceSharpness(flat, box))
	assert.Greater(t, faceSharpness(detailed, box), 1000.0)
	assert.Zero(t, faceSharpness(detailed, [4]float64{10, 10, 12, 12}), "tiny box")
}
