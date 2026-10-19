package tiles

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"doclite/client/internal/imgdecode"
)

// Options 是一次切块的上限。
type Options struct {
	// MaxOutputBytes：写出的块合计超过它就中止（too_large）；0 表示不限。几 KB 的压缩文件也能解出极大、
	// 极难压缩的图，不限的话会把缓存挤满。
	MaxOutputBytes int64
	// Workers：同时编码几块；0 表示按 CPU 数（最多 4）。
	Workers int
}

// Result 是切好的金字塔（imagecache 记进索引，分块描述按它拼）。
type Result struct {
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	MaxLevel  int    `json:"max_level"`
	Format    string `json:"format"`
	Mode      Mode   `json:"mode"`
	TileCount int    `json:"tile_count"`
	Bytes     int64  `json:"tiles_bytes"`
	ElapsedMs int64  `json:"elapsed_ms"`
}

// Generate 整张解码、转正，逐级减半，把块写到 outDir/<级>/<x>_<y>.<jpg|png>（outDir 要已经存在）。
// ctx 到期时中止（timeout）。失败时 outDir 里可能留下写了一半的块，由调用方删掉。
func (p *Plan) Generate(ctx context.Context, outDir string, opt Options) (*Result, error) {
	started := time.Now()
	// 大图在两个阶段之间主动回收一次：解码器读条带的缓冲、解码出来的图一用完就是垃圾，不回收的话 Go 的堆要涨到
	// 存活数据的两倍才回收（200 MB 的无压缩 TIFF 实测进程峰值从约 1.1 GB 降到估算的水平）。
	big := p.Estimate > 64<<20
	img, err := p.decode(ctx)
	if err != nil {
		return nil, err
	}
	if big {
		runtime.GC()
	}
	// 之后不再用 img：转正后的那一份建好后，解码出来的图就可以回收了（灰度、方向为 1 时两者是同一份像素）。
	lv, err := toLevel(ctx, img, p.channels(), p.info.Orientation)
	if err != nil {
		return nil, err
	}
	if big {
		runtime.GC()
	}
	if lv.w != p.Width || lv.h != p.Height {
		return nil, fail(imgdecode.KindCorrupt, "转正后是 %d×%d，文件头算出的是 %d×%d", lv.w, lv.h, p.Width, p.Height)
	}
	workers := opt.Workers
	if workers <= 0 {
		workers = min(4, max(1, runtime.NumCPU()))
	}
	w := &writer{ctx: ctx, dir: outDir, ext: p.Format, mode: p.Mode, limit: opt.MaxOutputBytes, workers: workers}
	maxLevel := MaxLevel(lv.w, lv.h)
	for l := maxLevel; l >= 0; l-- {
		if err := w.level(lv, l, l == maxLevel); err != nil {
			return nil, err
		}
		if l > 0 {
			if ctx.Err() != nil {
				return nil, timeoutError()
			}
			lv = halve(lv)
		}
	}
	return &Result{
		Width:     p.Width,
		Height:    p.Height,
		MaxLevel:  maxLevel,
		Format:    p.Format,
		Mode:      p.Mode,
		TileCount: int(w.count.Load()),
		Bytes:     w.bytes.Load(),
		ElapsedMs: time.Since(started).Milliseconds(),
	}, nil
}

// TilePath 是一块在金字塔目录里的相对路径。
func TilePath(level, x, y int, ext string) string {
	return filepath.Join(strconv.Itoa(level), strconv.Itoa(x)+"_"+strconv.Itoa(y)+"."+ext)
}

type writer struct {
	ctx     context.Context
	dir     string
	ext     string
	mode    Mode
	limit   int64
	workers int

	bytes atomic.Int64
	count atomic.Int64
}

// bilevelPalette：最高一级的黑白块编成 1 位的 PNG（Go 的 PNG 编码器见到两色的调色板图就写 1 位）。
var bilevelPalette = color.Palette{color.Gray{Y: 0}, color.Gray{Y: 255}}

// level 写一级的全部块（几个协程并行编码）。
func (w *writer) level(lv *level, l int, top bool) error {
	if err := os.MkdirAll(filepath.Join(w.dir, strconv.Itoa(l)), 0o755); err != nil {
		return fail(imgdecode.KindIO, "建不了块目录：%v", err)
	}
	cols, rows := Grid(lv.w, lv.h)
	jobs := make(chan [2]int)
	var (
		once     sync.Once
		firstErr error
		wg       sync.WaitGroup
	)
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	setErr := func(err error) {
		once.Do(func() {
			firstErr = err
			cancel()
		})
	}
	for range min(w.workers, cols*rows) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			enc := &tileEncoder{}
			for j := range jobs {
				if err := w.tile(ctx, enc, lv, l, j[0], j[1], top); err != nil {
					setErr(err)
					return
				}
			}
		}()
	}
loop:
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			select {
			case jobs <- [2]int{x, y}:
			case <-ctx.Done():
				break loop
			}
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if w.ctx.Err() != nil {
		return timeoutError()
	}
	return nil
}

// tileEncoder 是一个协程复用的缓冲。
type tileEncoder struct {
	buf  bytes.Buffer
	rgba *image.RGBA
	gray *image.Gray
	pal  *image.Paletted
}

func (w *writer) tile(ctx context.Context, enc *tileEncoder, lv *level, l, x, y int, top bool) error {
	if ctx.Err() != nil {
		if w.ctx.Err() != nil {
			return timeoutError()
		}
		return nil // 别的块已经出错
	}
	x0, x1 := TileSpan(x, lv.w)
	y0, y1 := TileSpan(y, lv.h)
	tw, th := x1-x0, y1-y0
	enc.buf.Reset()
	var err error
	switch {
	case lv.ch == 3:
		img := enc.rgbaOf(tw, th)
		for row := 0; row < th; row++ {
			src := lv.pix[((y0+row)*lv.w+x0)*3:]
			dst := img.Pix[row*img.Stride:]
			for i := 0; i < tw; i++ {
				dst[i*4], dst[i*4+1], dst[i*4+2], dst[i*4+3] = src[i*3], src[i*3+1], src[i*3+2], 255
			}
		}
		err = jpeg.Encode(&enc.buf, img, &jpeg.Options{Quality: JPEGQuality})
	case w.mode == ModeBilevel && top:
		img := enc.palOf(tw, th)
		for row := 0; row < th; row++ {
			src := lv.pix[(y0+row)*lv.w+x0:]
			dst := img.Pix[row*img.Stride:]
			for i := 0; i < tw; i++ {
				if src[i] >= 128 {
					dst[i] = 1
				} else {
					dst[i] = 0
				}
			}
		}
		err = pngEncoder.Encode(&enc.buf, img)
	default:
		img := enc.grayOf(tw, th)
		for row := 0; row < th; row++ {
			copy(img.Pix[row*img.Stride:row*img.Stride+tw], lv.pix[(y0+row)*lv.w+x0:])
		}
		if w.mode == ModeBilevel {
			err = pngEncoder.Encode(&enc.buf, img)
		} else {
			err = jpeg.Encode(&enc.buf, img, &jpeg.Options{Quality: JPEGQuality})
		}
	}
	if err != nil {
		return fail(imgdecode.KindIO, "编码块 %d/%d_%d 失败：%v", l, x, y, err)
	}
	total := w.bytes.Add(int64(enc.buf.Len()))
	if w.limit > 0 && total > w.limit {
		return fail(imgdecode.KindTooLarge, "块合计超过 %d MB", w.limit>>20)
	}
	if err := os.WriteFile(filepath.Join(w.dir, TilePath(l, x, y, w.ext)), enc.buf.Bytes(), 0o644); err != nil {
		return fail(imgdecode.KindIO, "写块失败：%v", err)
	}
	w.count.Add(1)
	return nil
}

var pngEncoder = &png.Encoder{CompressionLevel: png.DefaultCompression}

func (e *tileEncoder) rgbaOf(w, h int) *image.RGBA {
	if e.rgba == nil || e.rgba.Rect.Dx() != w || e.rgba.Rect.Dy() != h {
		e.rgba = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	return e.rgba
}

func (e *tileEncoder) grayOf(w, h int) *image.Gray {
	if e.gray == nil || e.gray.Rect.Dx() != w || e.gray.Rect.Dy() != h {
		e.gray = image.NewGray(image.Rect(0, 0, w, h))
	}
	return e.gray
}

func (e *tileEncoder) palOf(w, h int) *image.Paletted {
	if e.pal == nil || e.pal.Rect.Dx() != w || e.pal.Rect.Dy() != h {
		e.pal = image.NewPaletted(image.Rect(0, 0, w, h), bilevelPalette)
	}
	return e.pal
}

// String 方便日志。
func (r *Result) String() string {
	return fmt.Sprintf("%d×%d %s/%s %d 块 %d KB %d ms", r.Width, r.Height, r.Mode, r.Format, r.TileCount, r.Bytes>>10, r.ElapsedMs)
}
