package tiles

// level 是金字塔的一级：按行存的像素，每像素 ch 个字节（1 灰度、3 RGB），没有 alpha（已合成到白底）。
type level struct {
	w, h, ch int
	pix      []byte
}

// halve 缩成一半：2×2 取平均（四舍五入）；奇数边上的最后一列、一行按实际有的像素平均。
// 和 web/src/lib/viewer/pyramid.ts 的 halve 一致。
func halve(src *level) *level {
	w, h, ch := src.w, src.h, src.ch
	ow, oh := (w+1)/2, (h+1)/2
	out := make([]byte, ow*oh*ch)
	rowBytes := w * ch
	o := 0
	for oy := 0; oy < oh; oy++ {
		y0 := oy * 2
		twoRows := y0+1 < h
		r0 := y0 * rowBytes
		r1 := r0
		if twoRows {
			r1 += rowBytes
		}
		for ox := 0; ox < ow; ox++ {
			x0 := ox * 2
			twoCols := x0+1 < w
			n := 1
			if twoRows {
				n *= 2
			}
			if twoCols {
				n *= 2
			}
			half := n >> 1
			a := r0 + x0*ch
			b := r1 + x0*ch
			for c := 0; c < ch; c++ {
				s := int(src.pix[a+c])
				if twoCols {
					s += int(src.pix[a+ch+c])
				}
				if twoRows {
					s += int(src.pix[b+c])
					if twoCols {
						s += int(src.pix[b+ch+c])
					}
				}
				out[o] = byte((s + half) / n)
				o++
			}
		}
	}
	return &level{w: ow, h: oh, ch: ch, pix: out}
}
