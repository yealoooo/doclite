package bundle

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"io"
	"math/bits"

	"golang.org/x/image/tiff/lzw"
)

// TIFF 的无损转换（硬性规则 11 的例外）：自己解条带或分块（无压缩、LZW、Deflate、PackBits，解压用标准库和
// golang.org/x/image/tiff/lzw），撤销水平差分预测器，16 位转成大端，分平面的拼回每像素一组。整张图解到内存里
// （受 Options.ConvertMemoryLimit 约束），颜色空间和位深不变，透明度单独做成 SMask。
//
// 不用 golang.org/x/image/tiff 整张解码：它不支持分平面（会悄悄解错）、2 位和 4 位、CMYK、灰度加透明，
// 而且会把颜色展开成 RGBA。

// tiffConversion 检查能不能转换，估计内存。
func tiffConversion(l *tiffLayout, reason string) (*conversion, error) {
	switch l.compression {
	case compNone, compLZW, compDeflate, compDeflate2, compPackBits:
	default:
		return nil, unsupportedf("这一页要转换（%s），但这种压缩方式转换不了", reason)
	}
	if l.predictor == 2 && l.bits != 8 && l.bits != 16 {
		return nil, unsupportedf("不支持 %d 位样本用水平差分预测器的 TIFF", l.bits)
	}
	if l.predictor != 1 && l.predictor != 2 {
		return nil, unsupportedf("不支持 TIFF 预测器 %d", l.predictor)
	}
	if l.bits < 8 && (l.planes() > 1 || l.spp > l.colors) {
		return nil, unsupportedf("不支持 %d 位样本分平面存或带额外通道的 TIFF", l.bits)
	}
	if l.tiled && (l.blockW*l.planeSamples()*l.bits)%8 != 0 {
		return nil, unsupportedf("TIFF 分块的宽度不是整字节")
	}
	planeBytes := int64(l.height) * l.rowBytes(l.width)
	maxBlock, maxRaw := int64(0), int64(0)
	for by := range l.down {
		maxBlock = max(maxBlock, int64(l.blockRows(by))*l.rowBytes(l.blockW))
	}
	for _, c := range l.counts {
		maxRaw = max(maxRaw, int64(c))
	}
	return &conversion{
		reason: reason,
		memory: int64(l.planes())*planeBytes + maxBlock + maxRaw,
		decode: func(ctx context.Context) (*raster, error) { return decodeTIFF(ctx, l) },
	}, nil
}

func decodeTIFF(ctx context.Context, l *tiffLayout) (*raster, error) {
	nPlanes := l.planes()
	ps := l.planeSamples()
	rowBytes := l.rowBytes(l.width)
	planes := make([][]byte, nPlanes)
	for p := range planes {
		planes[p] = make([]byte, int64(l.height)*rowBytes)
	}
	blockRowBytes := l.rowBytes(l.blockW)
	var block []byte
	for p := range nPlanes {
		for by := range l.down {
			for bx := range l.across {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				idx := (p*l.down+by)*l.across + bx
				rows := l.blockRows(by)
				need := int64(rows) * blockRowBytes
				raw := make([]byte, l.counts[idx])
				if err := readFull(l.t.ra, raw, int64(l.offsets[idx])); err != nil {
					return nil, err
				}
				if l.fillOrder == 2 {
					for i, c := range raw {
						raw[i] = bits.Reverse8(c)
					}
				}
				if int64(cap(block)) < need {
					block = make([]byte, need)
				}
				block = block[:need]
				if err := decompressBlock(l.compression, raw, block); err != nil {
					return nil, corruptf("TIFF 第 %d 个条带解不开：%v", idx+1, err)
				}
				for r := range rows {
					row := block[int64(r)*blockRowBytes : int64(r+1)*blockRowBytes]
					if l.predictor == 2 {
						undoPredictor(row, l.bits, ps, l.t.bo)
					}
					if l.bits == 16 && l.t.bo == binary.LittleEndian {
						for i := 0; i+1 < len(row); i += 2 {
							row[i], row[i+1] = row[i+1], row[i]
						}
					}
					y := by*l.blockH + r
					if y >= l.height {
						break
					}
					x0 := bx * l.blockW
					w := min(l.blockW, l.width-x0)
					dst := int64(y)*rowBytes + int64(x0*ps*l.bits/8)
					copy(planes[p][dst:dst+l.rowBytes(w)], row)
				}
			}
		}
	}

	r := &raster{w: l.width, h: l.height, bpc: l.bits}
	r.cs, r.decode = tiffColorSpace(l)
	colors := l.colors
	expand := l.photometric == 3 && r.cs.lookup == nil // 16 位调色板：展开成 16 位 RGB
	if expand {
		r.cs, r.bpc = csRGB, 16
	}
	alpha := -1
	if l.spp > l.colors && len(l.extra) > 0 && (l.extra[0] == 1 || l.extra[0] == 2) {
		alpha = l.colors
		r.alphaBPC = l.bits
		r.matte = l.extra[0] == 1
	}
	sample := func(y, x, c int) uint32 {
		if nPlanes > 1 {
			return getSample(planes[c][int64(y)*rowBytes:], x, l.bits)
		}
		return getSample(planes[0][int64(y)*rowBytes:], x*l.spp+c, l.bits)
	}
	n := len(l.colormap) / 3
	r.fill = func(y int, color, alphaOut []uint32) {
		for x := range l.width {
			if color != nil {
				if expand {
					i := int(sample(y, x, 0))
					if i >= n {
						i = n - 1
					}
					color[3*x], color[3*x+1], color[3*x+2] = l.colormap[i], l.colormap[n+i], l.colormap[2*n+i]
				} else {
					for c := range colors {
						color[x*colors+c] = sample(y, x, c)
					}
				}
			}
			if alphaOut != nil {
				alphaOut[x] = sample(y, x, alpha)
			}
		}
	}
	return r, nil
}

// decompressBlock 把一个条带解压到 out（正好 len(out) 字节）；数据不够算损坏，多出来的不管。
func decompressBlock(compression int, raw, out []byte) error {
	switch compression {
	case compNone:
		if len(raw) < len(out) {
			return io.ErrUnexpectedEOF
		}
		copy(out, raw)
		return nil
	case compLZW:
		r := lzw.NewReader(bytes.NewReader(raw), lzw.MSB, 8)
		defer r.Close()
		_, err := io.ReadFull(r, out)
		return err
	case compDeflate, compDeflate2:
		r, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.ReadFull(r, out)
		return err
	case compPackBits:
		return unpackBits(raw, out)
	}
	return io.ErrUnexpectedEOF
}

// unpackBits 解 PackBits：n ≥ 0 时后面 n+1 个字节照抄，-127..-1 时下一个字节重复 1-n 次，-128 是空操作。
func unpackBits(raw, out []byte) error {
	o := 0
	for i := 0; i < len(raw) && o < len(out); {
		n := int8(raw[i])
		i++
		switch {
		case n >= 0:
			k := int(n) + 1
			if i+k > len(raw) {
				return io.ErrUnexpectedEOF
			}
			o += copy(out[o:], raw[i:i+k])
			i += k
		case n != -128:
			if i >= len(raw) {
				return io.ErrUnexpectedEOF
			}
			for k := 1 - int(n); k > 0 && o < len(out); k-- {
				out[o] = raw[i]
				o++
			}
			i++
		}
	}
	if o < len(out) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// undoPredictor 撤销一行的水平差分预测器（8 位、16 位；16 位按文件的字节序）。stride 是每像素的样本数。
func undoPredictor(row []byte, bitsPerSample, stride int, bo binary.ByteOrder) {
	if bitsPerSample == 8 {
		for i := stride; i < len(row); i++ {
			row[i] += row[i-stride]
		}
		return
	}
	for i := stride; 2*i+1 < len(row); i++ {
		v := bo.Uint16(row[2*i:]) + bo.Uint16(row[2*(i-stride):])
		bo.PutUint16(row[2*i:], v)
	}
}

// getSample 取一行里第 i 个样本（1、2、4、8 位高位在前，16 位大端）。
func getSample(row []byte, i, bitsPerSample int) uint32 {
	switch bitsPerSample {
	case 8:
		return uint32(row[i])
	case 16:
		return uint32(row[2*i])<<8 | uint32(row[2*i+1])
	}
	bit := i * bitsPerSample
	shift := 8 - bitsPerSample - bit%8
	return uint32(row[bit/8]>>shift) & (1<<bitsPerSample - 1)
}
