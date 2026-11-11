package dwpose

import (
	"context"
	"errors"
	"image"
	"math"
)

type Keypoint struct{ X, Y, Score float64 }

type Person [134]Keypoint

type Estimator interface {
	Estimate(ctx context.Context, img *image.RGBA) ([]Person, error)
}

var ErrUnavailable = errors.New("dwpose unavailable")

const scoreThreshold = 0.3

func CanvasSize(w, h, res int) (tw, th, cw, ch int) {
	k := float64(res) / float64(min(w, h))
	tw = int(math.RoundToEven(float64(w) * k))
	th = int(math.RoundToEven(float64(h) * k))
	return tw, th, ceil64(tw), ceil64(th)
}

func ceil64(x int) int {
	return int(math.Ceil(float64(x)/64.0) * 64)
}

var (
	mmposeIdx   = []int{17, 6, 8, 10, 7, 9, 12, 14, 16, 13, 15, 2, 1, 4, 3}
	openposeIdx = []int{1, 2, 3, 4, 6, 7, 8, 9, 10, 12, 13, 14, 15, 16, 17}
)

func Assemble(kp [133][2]float64, score [133]float64) Person {
	var withNeck Person
	neck := Keypoint{
		X: (kp[5][0] + kp[6][0]) / 2,
		Y: (kp[5][1] + kp[6][1]) / 2,
	}
	if score[5] > scoreThreshold && score[6] > scoreThreshold {
		neck.Score = 1
	}
	j := 0
	for i := 0; i < 134; i++ {
		if i == 17 {
			withNeck[i] = neck
			continue
		}
		withNeck[i] = Keypoint{X: kp[j][0], Y: kp[j][1], Score: score[j]}
		j++
	}
	out := withNeck
	for n, dst := range openposeIdx {
		out[dst] = withNeck[mmposeIdx[n]]
	}
	return out
}

type PersonJSON struct {
	Pose      []float64 `json:"pose_keypoints_2d"`
	Face      []float64 `json:"face_keypoints_2d"`
	HandLeft  []float64 `json:"hand_left_keypoints_2d"`
	HandRight []float64 `json:"hand_right_keypoints_2d"`
}

func part(kps []Keypoint) []float64 {
	any := false
	out := make([]float64, 0, len(kps)*3)
	for _, k := range kps {
		if k.Score >= scoreThreshold {
			any = true
			out = append(out, k.X, k.Y, 1)
		} else {
			out = append(out, 0, 0, 0)
		}
	}
	if !any {
		return nil
	}
	return out
}

func Format(p Person) PersonJSON {
	body := part(p[0:18])
	if body == nil {
		body = make([]float64, 18*3)
	}
	face := part(p[24:92])
	if face != nil {
		face = append(face, body[14*3:14*3+3]...)
		face = append(face, body[15*3:15*3+3]...)
	}
	return PersonJSON{
		Pose:      body,
		Face:      face,
		HandLeft:  part(p[92:113]),
		HandRight: part(p[113:134]),
	}
}
