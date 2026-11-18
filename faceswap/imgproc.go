package faceswap

import (
	"image"
	"math"
)

type plane struct {
	w, h, c int
	pix     []float32
}

func newPlane(w, h, c int) plane {
	return plane{w: w, h: h, c: c, pix: make([]float32, w*h*c)}
}

type fetchFn func(x, y, ch int) float32

func fromImage(im image.Image) plane {
	b := im.Bounds()
	p := newPlane(b.Dx(), b.Dy(), 3)
	set := func(x, y int, r, g, bb uint8) {
		o := (y*p.w + x) * 3
		p.pix[o], p.pix[o+1], p.pix[o+2] = float32(r), float32(g), float32(bb)
	}
	switch s := im.(type) {
	case *image.NRGBA:
		for y := 0; y < p.h; y++ {
			row := s.Pix[y*s.Stride:]
			for x := 0; x < p.w; x++ {
				set(x, y, row[4*x], row[4*x+1], row[4*x+2])
			}
		}
	case *image.RGBA:
		for y := 0; y < p.h; y++ {
			row := s.Pix[y*s.Stride:]
			for x := 0; x < p.w; x++ {
				set(x, y, row[4*x], row[4*x+1], row[4*x+2])
			}
		}
	default:
		for y := 0; y < p.h; y++ {
			for x := 0; x < p.w; x++ {
				r, g, bb, a := im.At(b.Min.X+x, b.Min.Y+y).RGBA()
				if a > 0 && a < 0xffff {
					r, g, bb = r*0xffff/a, g*0xffff/a, bb*0xffff/a
				}
				set(x, y, uint8(r>>8), uint8(g>>8), uint8(bb>>8))
			}
		}
	}
	return p
}

func (p plane) toImage() *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, p.w, p.h))
	for i := 0; i < p.w*p.h; i++ {
		for ch := 0; ch < 3; ch++ {
			out.Pix[4*i+ch] = uint8(clamp255(p.pix[3*i+ch]))
		}
		out.Pix[4*i+3] = 255
	}
	return out
}

func clamp255(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func round8(v float32) float32 { return clamp255(float32(math.RoundToEven(float64(v)))) }

func trunc8(v float32) float32 { return clamp255(float32(math.Floor(float64(v)))) }

func (p plane) at(x, y, ch int) float32 { return p.pix[(y*p.w+x)*p.c+ch] }

func (p plane) replicate(x, y, ch int) float32 {
	return p.at(clampInt(x, 0, p.w-1), clampInt(y, 0, p.h-1), ch)
}

func (p plane) constant(v float32) fetchFn {
	return func(x, y, ch int) float32 {
		if x < 0 || y < 0 || x >= p.w || y >= p.h {
			return v
		}
		return p.at(x, y, ch)
	}
}

func (p plane) padded(pad int) fetchFn {
	return func(x, y, ch int) float32 {
		x, y = x-pad, y-pad
		if x >= 0 && y >= 0 && x < p.w && y < p.h {
			return p.at(x, y, ch)
		}
		if x >= -pad && y >= -pad && x < p.w+pad && y < p.h+pad {
			return 128
		}
		return 0
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func resizeLinear(src fetchFn, sw, sh, c, dw, dh int) plane {
	out := newPlane(dw, dh, c)
	axis := func(d, sn, dn int) (int, int, float32) {
		f := (float64(d)+0.5)*float64(sn)/float64(dn) - 0.5
		i := int(math.Floor(f))
		a := float32(f - float64(i))
		if i < 0 {
			i, a = 0, 0
		}
		if i >= sn-1 {
			i, a = sn-1, 0
		}
		return i, min(i+1, sn-1), a
	}
	for y := 0; y < dh; y++ {
		y0, y1, ay := axis(y, sh, dh)
		for x := 0; x < dw; x++ {
			x0, x1, ax := axis(x, sw, dw)
			for ch := 0; ch < c; ch++ {
				top := src(x0, y0, ch)*(1-ax) + src(x1, y0, ch)*ax
				bot := src(x0, y1, ch)*(1-ax) + src(x1, y1, ch)*ax
				out.pix[(y*dw+x)*c+ch] = round8(top*(1-ay) + bot*ay)
			}
		}
	}
	return out
}

type affine [6]float64

func (m affine) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[1]*y + m[2], m[3]*x + m[4]*y + m[5]
}

func (m affine) invert() affine {
	d := m[0]*m[4] - m[1]*m[3]
	if d != 0 {
		d = 1 / d
	}
	a, b, c, e := m[4]*d, -m[1]*d, -m[3]*d, m[0]*d
	return affine{a, b, -a*m[2] - b*m[5], c, e, -c*m[2] - e*m[5]}
}

func similarity(src, dst [5][2]float64) affine {
	var ms, md [2]float64
	for i := range src {
		for k := 0; k < 2; k++ {
			ms[k] += src[i][k] / 5
			md[k] += dst[i][k] / 5
		}
	}
	var num1, num2, den float64
	for i := range src {
		sx, sy := src[i][0]-ms[0], src[i][1]-ms[1]
		dx, dy := dst[i][0]-md[0], dst[i][1]-md[1]
		num1 += sx*dx + sy*dy
		num2 += sx*dy - sy*dx
		den += sx*sx + sy*sy
	}
	a, b := num1/den, num2/den
	return affine{a, -b, md[0] - a*ms[0] + b*ms[1], b, a, md[1] - b*ms[0] - a*ms[1]}
}

func warp(src fetchFn, c int, m affine, dw, dh int, quant bool) plane {
	out := newPlane(dw, dh, c)
	warpInto(out, src, m.invert(), 0, 0, dw, dh, quant, nil)
	return out
}

func warpInto(out plane, src fetchFn, inv affine, x0, y0, x1, y1 int, quant bool, blend func(i, ch int, v float32)) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			sx, sy := inv.apply(float64(x), float64(y))
			qx, qy := int(math.Round(sx*32)), int(math.Round(sy*32))
			ix, iy := qx>>5, qy>>5
			fx, fy := float32(qx&31)/32, float32(qy&31)/32
			i := y*out.w + x
			for ch := 0; ch < out.c; ch++ {
				top := src(ix, iy, ch)*(1-fx) + src(ix+1, iy, ch)*fx
				bot := src(ix, iy+1, ch)*(1-fx) + src(ix+1, iy+1, ch)*fx
				v := top*(1-fy) + bot*fy
				if quant {
					v = round8(v)
				}
				if blend != nil {
					blend(i, ch, v)
				} else {
					out.pix[i*out.c+ch] = v
				}
			}
		}
	}
}

func pasteBack(dst plane, part plane, m affine, mask plane) {
	inv := m
	x0, y0, x1, y1 := dst.w, dst.h, 0, 0
	back := m.invert()
	for _, c := range [][2]float64{{0, 0}, {float64(part.w), 0}, {0, float64(part.h)}, {float64(part.w), float64(part.h)}} {
		x, y := back.apply(c[0], c[1])
		x0, y0 = min(x0, int(math.Floor(x))-1), min(y0, int(math.Floor(y))-1)
		x1, y1 = max(x1, int(math.Ceil(x))+2), max(y1, int(math.Ceil(y))+2)
	}
	x0, y0 = clampInt(x0, 0, dst.w), clampInt(y0, 0, dst.h)
	x1, y1 = clampInt(x1, x0, dst.w), clampInt(y1, y0, dst.h)
	w, h := x1-x0, y1-y0
	if w == 0 || h == 0 {
		return
	}
	shift := affine{inv[0], inv[1], inv[0]*float64(x0) + inv[1]*float64(y0) + inv[2], inv[3], inv[4], inv[3]*float64(x0) + inv[4]*float64(y0) + inv[5]}
	warped := newPlane(w, h, 3)
	warpInto(warped, part.replicate, shift, 0, 0, w, h, true, nil)
	alpha := newPlane(w, h, 1)
	warpInto(alpha, mask.constant(0), shift, 0, 0, w, h, false, nil)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := alpha.pix[y*w+x]
			o := ((y+y0)*dst.w + x + x0) * 3
			for ch := 0; ch < 3; ch++ {
				dst.pix[o+ch] = trunc8(warped.pix[(y*w+x)*3+ch]*a + dst.pix[o+ch]*(1-a))
			}
		}
	}
}

func gaussKernel(k int) []float32 {
	sigma := 0.3*(float64(k-1)*0.5-1) + 0.8
	out := make([]float32, k)
	var sum float64
	vals := make([]float64, k)
	for i := range vals {
		x := float64(i) - float64(k-1)/2
		vals[i] = math.Exp(-x * x / (2 * sigma * sigma))
		sum += vals[i]
	}
	for i := range vals {
		out[i] = float32(vals[i] / sum)
	}
	return out
}

func reflect101(i, n int) int {
	if n == 1 {
		return 0
	}
	for i < 0 || i >= n {
		if i < 0 {
			i = -i
		}
		if i >= n {
			i = 2*n - 2 - i
		}
	}
	return i
}

func blur(p plane, k int) plane {
	kern := gaussKernel(k)
	r := k / 2
	tmp := newPlane(p.w, p.h, 1)
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			var s float32
			for i, kv := range kern {
				s += kv * p.pix[y*p.w+reflect101(x+i-r, p.w)]
			}
			tmp.pix[y*p.w+x] = s
		}
	}
	out := newPlane(p.w, p.h, 1)
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			var s float32
			for i, kv := range kern {
				s += kv * tmp.pix[reflect101(y+i-r, p.h)*p.w+x]
			}
			out.pix[y*p.w+x] = s
		}
	}
	return out
}

func erode(p plane, k int) plane {
	r := k / 2
	pass := func(src plane, dx, dy int) plane {
		out := newPlane(src.w, src.h, 1)
		for y := 0; y < src.h; y++ {
			for x := 0; x < src.w; x++ {
				v := float32(math.Inf(1))
				for i := -r; i <= r; i++ {
					xx, yy := x+i*dx, y+i*dy
					if xx >= 0 && yy >= 0 && xx < src.w && yy < src.h {
						v = min(v, src.pix[yy*src.w+xx])
					}
				}
				out.pix[y*src.w+x] = v
			}
		}
		return out
	}
	return pass(pass(p, 1, 0), 0, 1)
}

func boxMask(size int) plane {
	m := newPlane(size, size, 1)
	pad := int(float64(size) * 0.08)
	for y := pad; y < size-pad; y++ {
		for x := pad; x < size-pad; x++ {
			m.pix[y*size+x] = 1
		}
	}
	return blur(m, int(float64(size)*0.15)|1)
}

func chw(p plane, f func(v float32, ch int) float32) []float32 {
	n := p.w * p.h
	out := make([]float32, n*p.c)
	for i := 0; i < n; i++ {
		for ch := 0; ch < p.c; ch++ {
			out[ch*n+i] = f(p.pix[i*p.c+ch], ch)
		}
	}
	return out
}

func fromCHW(data []float32, w, h int, f func(v float32) float32) plane {
	p := newPlane(w, h, 3)
	n := w * h
	for i := 0; i < n; i++ {
		for ch := 0; ch < 3; ch++ {
			p.pix[i*3+ch] = f(data[ch*n+i])
		}
	}
	return p
}
