package dwpose

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	ort "github.com/getcharzp/onnxruntime_purego"
)

const (
	detSize    = 640
	poseW      = 288
	poseH      = 384
	simccSplit = 2.0
)

var (
	poseMean = [3]float64{123.675, 116.28, 103.53}
	poseStd  = [3]float64{58.395, 57.12, 57.375}
)

type Config struct {
	LibPath   string
	ModelsDir string
	Threads   int
}

type ONNXEstimator struct {
	cfg  Config
	mu   sync.Mutex
	eng  *ort.Engine
	det  *ort.Session
	pose *ort.Session
}

func NewONNX(cfg Config) *ONNXEstimator {
	return &ONNXEstimator{cfg: cfg}
}

func libCandidates(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	var names []string
	switch runtime.GOOS {
	case "windows":
		names = []string{"onnxruntime.dll"}
	case "darwin":
		names = []string{"libonnxruntime.dylib"}
	default:
		names = []string{"libonnxruntime.so", "libonnxruntime.so.1"}
	}
	var out []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, n := range names {
			out = append(out, filepath.Join(dir, n), filepath.Join(dir, "onnxruntime", n))
		}
	}
	if runtime.GOOS == "linux" {
		out = append(out, names...)
	}
	return out
}

func (e *ONNXEstimator) init() error {
	if e.pose != nil {
		return nil
	}
	var eng *ort.Engine
	var lastErr error
	for _, p := range libCandidates(e.cfg.LibPath) {
		if filepath.IsAbs(p) {
			if _, err := os.Stat(p); err != nil {
				lastErr = err
				continue
			}
		}
		en, err := ort.NewEngine(p)
		if err == nil {
			eng = en
			break
		}
		lastErr = err
	}
	if eng == nil {
		return fmt.Errorf("%w: load ONNX Runtime: %v", ErrUnavailable, lastErr)
	}
	newSession := func(name string) (*ort.Session, error) {
		opts, err := eng.NewSessionOptions()
		if err != nil {
			return nil, err
		}
		defer opts.Close()
		if e.cfg.Threads > 0 {
			if err := opts.SetIntraOpNumThreads(e.cfg.Threads); err != nil {
				return nil, err
			}
		}
		return eng.NewSession(filepath.Join(e.cfg.ModelsDir, name), opts)
	}
	det, err := newSession("yolox_l.onnx")
	if err != nil {
		eng.Close()
		return fmt.Errorf("%w: load yolox_l.onnx: %v", ErrUnavailable, err)
	}
	pose, err := newSession("dw-ll_ucoco_384.onnx")
	if err != nil {
		det.Close()
		eng.Close()
		return fmt.Errorf("%w: load dw-ll_ucoco_384.onnx: %v", ErrUnavailable, err)
	}
	e.eng, e.det, e.pose = eng, det, pose
	return nil
}

func (e *ONNXEstimator) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pose != nil {
		e.pose.Close()
		e.det.Close()
		e.eng.Close()
	}
	e.eng, e.det, e.pose = nil, nil, nil
}

func (e *ONNXEstimator) Estimate(ctx context.Context, img *image.RGBA) ([]Person, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.init(); err != nil {
		return nil, err
	}
	src := toPlane(img)
	boxes, found, err := e.detect(ctx, src)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if len(boxes) == 0 {
		boxes = [][4]float64{{0, 0, float64(src.w), float64(src.h)}}
	}
	return e.estimatePoses(ctx, src, boxes)
}

func run(ctx context.Context, s *ort.Session, data []float32, shape []int64) ([][]float32, [][]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	in, err := ort.NewTensor(shape, data)
	if err != nil {
		return nil, nil, err
	}
	defer in.Close()
	outs, err := s.Run(map[string]*ort.Value{s.Inputs[0].Name: in})
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		for _, o := range outs {
			o.Close()
		}
	}()
	var datas [][]float32
	var shapes [][]int64
	for _, info := range s.Outputs {
		v, ok := outs[info.Name]
		if !ok {
			return nil, nil, fmt.Errorf("output %q missing", info.Name)
		}
		d, err := ort.GetTensorData[float32](v)
		if err != nil {
			return nil, nil, err
		}
		sh, err := v.GetShape()
		if err != nil {
			return nil, nil, err
		}
		datas = append(datas, append([]float32(nil), d...))
		shapes = append(shapes, append([]int64(nil), sh...))
	}
	return datas, shapes, nil
}

func (e *ONNXEstimator) detect(ctx context.Context, src plane) ([][4]float64, bool, error) {
	r := math.Min(float64(detSize)/float64(src.h), float64(detSize)/float64(src.w))
	rw, rh := int(float64(src.w)*r), int(float64(src.h)*r)
	resized := resizeLinearU8(src, rw, rh)
	input := make([]float32, 3*detSize*detSize)
	for i := range input {
		input[i] = 114
	}
	for y := 0; y < rh; y++ {
		for x := 0; x < rw; x++ {
			o := (y*rw + x) * 3
			for c := 0; c < 3; c++ {
				input[c*detSize*detSize+y*detSize+x] = float32(resized.pix[o+c])
			}
		}
	}
	outs, shapes, err := run(ctx, e.det, input, []int64{1, 3, detSize, detSize})
	if err != nil {
		return nil, false, fmt.Errorf("yolox: %w", err)
	}
	pred, shape := outs[0], shapes[0]
	if len(shape) != 3 || shape[2] < 6 {
		return nil, false, fmt.Errorf("yolox: unexpected output shape %v", shape)
	}
	n, stride := int(shape[1]), int(shape[2])
	numClasses := stride - 5
	type cell struct{ gx, gy, s float64 }
	cells := make([]cell, 0, n)
	for _, s := range []int{8, 16, 32} {
		hs, ws := detSize/s, detSize/s
		for y := 0; y < hs; y++ {
			for x := 0; x < ws; x++ {
				cells = append(cells, cell{float64(x), float64(y), float64(s)})
			}
		}
	}
	if len(cells) != n {
		return nil, false, fmt.Errorf("yolox: %d predictions, expected %d", n, len(cells))
	}
	boxes := make([][4]float64, n)
	scores := make([][]float64, numClasses)
	for c := range scores {
		scores[c] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		p := pred[i*stride : (i+1)*stride]
		cx := (float64(p[0]) + cells[i].gx) * cells[i].s
		cy := (float64(p[1]) + cells[i].gy) * cells[i].s
		w := math.Exp(float64(p[2])) * cells[i].s
		h := math.Exp(float64(p[3])) * cells[i].s
		boxes[i] = [4]float64{(cx - w/2) / r, (cy - h/2) / r, (cx + w/2) / r, (cy + h/2) / r}
		obj := float64(p[4])
		for c := 0; c < numClasses; c++ {
			scores[c][i] = obj * float64(p[5+c])
		}
	}
	anyDet := false
	var persons [][4]float64
	for c := 0; c < numClasses; c++ {
		var idx []int
		for i := 0; i < n; i++ {
			if scores[c][i] > 0.1 {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			continue
		}
		keep := nms(boxes, scores[c], idx, 0.45)
		if len(keep) > 0 {
			anyDet = true
		}
		if c != 0 {
			continue
		}
		for _, k := range keep {
			if scores[c][k] > 0.3 {
				persons = append(persons, boxes[k])
			}
		}
	}
	return persons, anyDet, nil
}

func nms(boxes [][4]float64, scores []float64, idx []int, thr float64) []int {
	order := append([]int(nil), idx...)
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	area := func(b [4]float64) float64 { return (b[2] - b[0] + 1) * (b[3] - b[1] + 1) }
	var keep []int
	for len(order) > 0 {
		i := order[0]
		keep = append(keep, i)
		var rest []int
		for _, j := range order[1:] {
			xx1 := math.Max(boxes[i][0], boxes[j][0])
			yy1 := math.Max(boxes[i][1], boxes[j][1])
			xx2 := math.Min(boxes[i][2], boxes[j][2])
			yy2 := math.Min(boxes[i][3], boxes[j][3])
			w := math.Max(0, xx2-xx1+1)
			h := math.Max(0, yy2-yy1+1)
			inter := w * h
			if inter/(area(boxes[i])+area(boxes[j])-inter) <= thr {
				rest = append(rest, j)
			}
		}
		order = rest
	}
	return keep
}

func (e *ONNXEstimator) estimatePoses(ctx context.Context, src plane, boxes [][4]float64) ([]Person, error) {
	n := len(boxes)
	input := make([]float32, n*3*poseW*poseH)
	centers := make([][2]float64, n)
	scales := make([][2]float64, n)
	for b, box := range boxes {
		cx, cy := (box[0]+box[2])/2, (box[1]+box[3])/2
		sw, sh := (box[2]-box[0])*1.25, (box[3]-box[1])*1.25
		aspect := float64(poseW) / float64(poseH)
		if sw > sh*aspect {
			sh = sw / aspect
		} else {
			sw = sh * aspect
		}
		centers[b] = [2]float64{cx, cy}
		scales[b] = [2]float64{sw, sh}
		m := cropMatrix(cx, cy, sw, poseW, poseH)
		crop := warpAffine(src, m, poseW, poseH)
		base := b * 3 * poseW * poseH
		for y := 0; y < poseH; y++ {
			for x := 0; x < poseW; x++ {
				o := (y*poseW + x) * 3
				for c := 0; c < 3; c++ {
					input[base+c*poseW*poseH+y*poseW+x] = float32((crop.pix[o+c] - poseMean[c]) / poseStd[c])
				}
			}
		}
	}
	outs, shapes, err := run(ctx, e.pose, input, []int64{int64(n), 3, poseH, poseW})
	if err != nil {
		return nil, fmt.Errorf("dwpose: %w", err)
	}
	if len(outs) < 2 || len(shapes[0]) != 3 || len(shapes[1]) != 3 || shapes[0][1] != 133 {
		return nil, fmt.Errorf("dwpose: unexpected outputs %v", shapes)
	}
	wx, wy := int(shapes[0][2]), int(shapes[1][2])
	people := make([]Person, n)
	for b := 0; b < n; b++ {
		var kp [133][2]float64
		var sc [133]float64
		for k := 0; k < 133; k++ {
			sx := outs[0][(b*133+k)*wx : (b*133+k+1)*wx]
			sy := outs[1][(b*133+k)*wy : (b*133+k+1)*wy]
			ix, vx := argmax(sx)
			iy, vy := argmax(sy)
			v := vx
			if vx > vy {
				v = vy
			}
			lx, ly := float64(ix), float64(iy)
			if v <= 0 {
				lx, ly = -1, -1
			}
			lx /= simccSplit
			ly /= simccSplit
			kp[k][0] = lx/float64(poseW)*scales[b][0] + centers[b][0] - scales[b][0]/2
			kp[k][1] = ly/float64(poseH)*scales[b][1] + centers[b][1] - scales[b][1]/2
			sc[k] = float64(v)
		}
		people[b] = Assemble(kp, sc)
	}
	return people, nil
}

func argmax(v []float32) (int, float32) {
	best, bi := v[0], 0
	for i, x := range v {
		if x > best {
			best, bi = x, i
		}
	}
	return bi, best
}
