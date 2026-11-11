package dwpose

import (
	"image"
	"image/draw"
	"math"
)

type plane struct {
	w, h int
	pix  []float64
}

func toRGB(img image.Image) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Over)
	for i := 3; i < len(out.Pix); i += 4 {
		out.Pix[i] = 255
	}
	return out
}

func toPlane(img *image.RGBA) plane {
	b := img.Bounds()
	p := plane{w: b.Dx(), h: b.Dy(), pix: make([]float64, b.Dx()*b.Dy()*3)}
	for y := 0; y < p.h; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < p.w; x++ {
			o := (y*p.w + x) * 3
			p.pix[o] = float64(row[x*4])
			p.pix[o+1] = float64(row[x*4+1])
			p.pix[o+2] = float64(row[x*4+2])
		}
	}
	return p
}

func fromPlane(p plane) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, p.w, p.h))
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			o := (y*p.w + x) * 3
			d := y*out.Stride + x*4
			out.Pix[d] = clampByte(p.pix[o])
			out.Pix[d+1] = clampByte(p.pix[o+1])
			out.Pix[d+2] = clampByte(p.pix[o+2])
			out.Pix[d+3] = 255
		}
	}
	return out
}

func clampByte(v float64) uint8 {
	v = math.RoundToEven(v)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func quantize(p plane) plane {
	for i, v := range p.pix {
		p.pix[i] = float64(clampByte(v))
	}
	return p
}

type kernel func(src plane, dw, dh int) plane

func cubicWeight(x float64) float64 {
	const a = -0.75
	x = math.Abs(x)
	switch {
	case x <= 1:
		return ((a+2)*x-(a+3))*x*x + 1
	case x < 2:
		return ((a*x-5*a)*x+8*a)*x - 4*a
	}
	return 0
}

type tap struct {
	idx []int
	w   []float64
}

func cubicTaps(sn, dn int) []tap {
	scale := float64(sn) / float64(dn)
	taps := make([]tap, dn)
	for d := 0; d < dn; d++ {
		fx := (float64(d)+0.5)*scale - 0.5
		ix := int(math.Floor(fx))
		t := fx - float64(ix)
		var tp tap
		for k := -1; k <= 2; k++ {
			tp.idx = append(tp.idx, clampInt(ix+k, 0, sn-1))
			tp.w = append(tp.w, cubicWeight(float64(k)-t))
		}
		taps[d] = tp
	}
	return taps
}

func areaTaps(sn, dn int) []tap {
	scale := float64(sn) / float64(dn)
	taps := make([]tap, dn)
	for d := 0; d < dn; d++ {
		f0 := float64(d) * scale
		f1 := f0 + scale
		var tp tap
		for s := int(math.Floor(f0)); s < int(math.Ceil(f1)) && s < sn; s++ {
			lo := math.Max(f0, float64(s))
			hi := math.Min(f1, float64(s+1))
			if hi > lo {
				tp.idx = append(tp.idx, s)
				tp.w = append(tp.w, (hi-lo)/scale)
			}
		}
		taps[d] = tp
	}
	return taps
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

func separable(src plane, dw, dh int, mk func(sn, dn int) []tap) plane {
	tx := mk(src.w, dw)
	ty := mk(src.h, dh)
	tmp := plane{w: dw, h: src.h, pix: make([]float64, dw*src.h*3)}
	for y := 0; y < src.h; y++ {
		for x := 0; x < dw; x++ {
			var r, g, b float64
			for k, sx := range tx[x].idx {
				o := (y*src.w + sx) * 3
				wt := tx[x].w[k]
				r += src.pix[o] * wt
				g += src.pix[o+1] * wt
				b += src.pix[o+2] * wt
			}
			o := (y*dw + x) * 3
			tmp.pix[o], tmp.pix[o+1], tmp.pix[o+2] = r, g, b
		}
	}
	out := plane{w: dw, h: dh, pix: make([]float64, dw*dh*3)}
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, b float64
			for k, sy := range ty[y].idx {
				o := (sy*dw + x) * 3
				wt := ty[y].w[k]
				r += tmp.pix[o] * wt
				g += tmp.pix[o+1] * wt
				b += tmp.pix[o+2] * wt
			}
			o := (y*dw + x) * 3
			out.pix[o], out.pix[o+1], out.pix[o+2] = r, g, b
		}
	}
	return quantize(out)
}

func resizeCubic(src plane, dw, dh int) plane {
	return separable(src, dw, dh, cubicTaps)
}

func resizeArea(src plane, dw, dh int) plane {
	return separable(src, dw, dh, areaTaps)
}

func padEdge(src plane, cw, ch int) plane {
	out := plane{w: cw, h: ch, pix: make([]float64, cw*ch*3)}
	for y := 0; y < ch; y++ {
		sy := min(y, src.h-1)
		for x := 0; x < cw; x++ {
			sx := min(x, src.w-1)
			copy(out.pix[(y*cw+x)*3:(y*cw+x)*3+3], src.pix[(sy*src.w+sx)*3:(sy*src.w+sx)*3+3])
		}
	}
	return out
}

func fitCanvas(img *image.RGBA, res int) *image.RGBA {
	b := img.Bounds()
	tw, th, cw, ch := CanvasSize(b.Dx(), b.Dy(), res)
	src := toPlane(img)
	var r plane
	if float64(res)/float64(min(b.Dx(), b.Dy())) > 1 {
		r = resizeCubic(src, tw, th)
	} else {
		r = resizeArea(src, tw, th)
	}
	return fromPlane(padEdge(r, cw, ch))
}

func fma32(a, b, c float32) float32 {
	return float32(math.FMA(float64(a), float64(b), float64(c)))
}

func affineFromPoints(src, dst [3][2]float32) [6]float64 {
	var m [6]float64
	x0, y0 := float64(src[0][0]), float64(src[0][1])
	x1, y1 := float64(src[1][0]), float64(src[1][1])
	x2, y2 := float64(src[2][0]), float64(src[2][1])
	det := x0*(y1-y2) - x1*(y0-y2) + x2*(y0-y1)
	for r := 0; r < 2; r++ {
		u0, u1, u2 := float64(dst[0][r]), float64(dst[1][r]), float64(dst[2][r])
		m[r*3] = (u0*(y1-y2) - u1*(y0-y2) + u2*(y0-y1)) / det
		m[r*3+1] = (x0*(u1-u2) - x1*(u0-u2) + x2*(u0-u1)) / det
		m[r*3+2] = (x0*(y1*u2-y2*u1) - x1*(y0*u2-y2*u0) + x2*(y0*u1-y1*u0)) / det
	}
	return m
}

func thirdPoint(a, b [2]float32) [2]float32 {
	dx, dy := a[0]-b[0], a[1]-b[1]
	return [2]float32{b[0] - dy, b[1] + dx}
}

func cropMatrix(cx, cy, sw float64, ow, oh int) [6]float64 {
	var src, dst [3][2]float32
	src[0] = [2]float32{float32(cx), float32(cy)}
	src[1] = [2]float32{float32(cx), float32(cy - sw*0.5)}
	src[2] = thirdPoint(src[0], src[1])
	w2, h2 := float64(ow)*0.5, float64(oh)*0.5
	dst[0] = [2]float32{float32(w2), float32(h2)}
	dst[1] = [2]float32{float32(w2), float32(h2 - w2)}
	dst[2] = thirdPoint(dst[0], dst[1])
	return affineFromPoints(src, dst)
}

func warpAffine(src plane, m [6]float64, dw, dh int) plane {
	d := m[0]*m[4] - m[1]*m[3]
	if d != 0 {
		d = 1 / d
	}
	a11, a22 := m[4]*d, m[0]*d
	a12, a21 := -m[1]*d, -m[3]*d
	b1 := -a11*m[2] - a12*m[5]
	b2 := -a21*m[2] - a22*m[5]
	M := [6]float32{float32(a11), float32(a12), float32(b1), float32(a21), float32(a22), float32(b2)}
	out := plane{w: dw, h: dh, pix: make([]float64, dw*dh*3)}
	at := func(x, y, c int) float32 {
		if x < 0 || y < 0 || x >= src.w || y >= src.h {
			return 0
		}
		return float32(src.pix[(y*src.w+x)*3+c])
	}
	for y := 0; y < dh; y++ {
		mx := fma32(M[1], float32(y), M[2])
		my := fma32(M[4], float32(y), M[5])
		for x := 0; x < dw; x++ {
			sx := fma32(M[0], float32(x), mx)
			sy := fma32(M[3], float32(x), my)
			ix, iy := int(math.Floor(float64(sx))), int(math.Floor(float64(sy)))
			al, be := sx-float32(ix), sy-float32(iy)
			o := (y*dw + x) * 3
			for c := 0; c < 3; c++ {
				f00, f01 := at(ix, iy, c), at(ix+1, iy, c)
				f10, f11 := at(ix, iy+1, c), at(ix+1, iy+1, c)
				f00 = fma32(al, f01-f00, f00)
				f10 = fma32(al, f11-f10, f10)
				out.pix[o+c] = float64(clampByte(float64(fma32(be, f10-f00, f00))))
			}
		}
	}
	return out
}

func linearCoefs(sn, dn int) ([]int, []int32) {
	scale := float64(sn) / float64(dn)
	idx := make([]int, dn)
	a := make([]int32, dn*2)
	for d := 0; d < dn; d++ {
		fx := float32((float64(d)+0.5)*scale - 0.5)
		sx := int(math.Floor(float64(fx)))
		fx -= float32(sx)
		if sx < 0 {
			sx, fx = 0, 0
		}
		if sx >= sn-1 {
			sx, fx = sn-1, 0
		}
		idx[d] = sx
		a[d*2] = int32(math.RoundToEven(float64((1 - fx) * 2048)))
		a[d*2+1] = int32(math.RoundToEven(float64(fx * 2048)))
	}
	return idx, a
}

func resizeLinearU8(src plane, dw, dh int) plane {
	xi, xa := linearCoefs(src.w, dw)
	yi, ya := linearCoefs(src.h, dh)
	rows := make([][]int32, src.h)
	hrow := func(sy int) []int32 {
		if rows[sy] != nil {
			return rows[sy]
		}
		r := make([]int32, dw*3)
		for x := 0; x < dw; x++ {
			s0 := xi[x]
			s1 := min(s0+1, src.w-1)
			for c := 0; c < 3; c++ {
				r[x*3+c] = int32(src.pix[(sy*src.w+s0)*3+c])*xa[x*2] + int32(src.pix[(sy*src.w+s1)*3+c])*xa[x*2+1]
			}
		}
		rows[sy] = r
		return r
	}
	out := plane{w: dw, h: dh, pix: make([]float64, dw*dh*3)}
	for y := 0; y < dh; y++ {
		s0 := yi[y]
		r0, r1 := hrow(s0), hrow(min(s0+1, src.h-1))
		b0, b1 := ya[y*2], ya[y*2+1]
		for e := 0; e < dw*3; e++ {
			v := (((r0[e]>>4)*b0)>>16 + ((r1[e]>>4)*b1)>>16 + 2) >> 2
			out.pix[y*dw*3+e] = float64(clampByte(float64(v)))
		}
	}
	return out
}
