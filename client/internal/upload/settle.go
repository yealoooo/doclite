package upload

import (
	"bytes"
	"io"
	"os"
	"time"
)

// 写完判定（requirements 第 6 节"热文件夹"）：大小和修改时间 2 秒不变 → 能独占打开（Windows；macOS 跳过）→
// 文件尾完整（JPEG 有 FF D9、PNG 有 IEND）；60 秒还不完整就记失败。规则和上一版、页面（lib/rescan.ts 的
// tailComplete）一致。
const (
	// SettleQuiet：大小和修改时间这么久不变才往下判。
	SettleQuiet = 2 * time.Second
	// SettleGiveUp：不变这么久文件尾还不完整就记失败（一直不能独占打开的只提示"被占用"，仍在等）。
	SettleGiveUp = 60 * time.Second
	// tailSlack：文件尾检查的余量（JPEG 的 EOI、PNG 的 IEND 之后允许有这么多多余的字节）。
	tailSlack = 4096
)

var (
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
	jpegEOI   = []byte{0xFF, 0xD9}
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	// pngIEND 是 IEND 块的类型和 CRC。
	pngIEND = []byte{0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}
)

// TailComplete 判断文件尾是否完整：JPEG（魔数 FF D8 FF）要在最后 4 KiB + 2 字节里有 EOI；PNG 要在最后 4 KiB + 12 字节
// 里有 IEND 块（含 CRC）；TIFF 和别的格式不查。空文件、只写了魔数开头几个字节的算不完整。只读开头 8 字节和末尾几 KiB。
func TailComplete(r io.ReaderAt, size int64) (bool, error) {
	head := make([]byte, min(size, 8))
	if _, err := r.ReadAt(head, 0); err != nil && err != io.EOF {
		return false, err
	}
	var marker []byte
	var window int64
	switch {
	case bytes.HasPrefix(head, jpegMagic):
		marker, window = jpegEOI, tailSlack+2
	case len(head) == 8 && bytes.Equal(head, pngMagic):
		marker, window = pngIEND, tailSlack+12
	case len(head) < 8 && (bytes.HasPrefix(pngMagic, head) || bytes.HasPrefix(jpegMagic, head[:min(len(head), 3)])):
		// 只写了开头几个字节（含空文件）。
		return false, nil
	default:
		return true, nil
	}
	n := min(size, window)
	tail := make([]byte, n)
	if _, err := r.ReadAt(tail, size-n); err != nil && err != io.EOF {
		return false, err
	}
	return bytes.Contains(tail, marker), nil
}

// TailCompleteFile 是 [TailComplete] 读本机文件的写法。
func TailCompleteFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	return TailComplete(f, st.Size())
}

// Verdict 是一次写完判定的结论。
type Verdict int

const (
	// KeepWaiting：还在写，或者还没到时间，下次再看。
	KeepWaiting Verdict = iota
	// Ready：写完了，可以排队上传。
	Ready
	// Incomplete：不变超过 60 秒，文件尾仍不完整：记失败（incomplete_file）。文件之后再变会重新判定。
	Incomplete
	// Locked：不变超过 60 秒，但一直不能独占打开：只提示"被其他程序占用"，仍在等。
	Locked
)

// Settle 是一个等待写完的文件的观察记录（只在内存里；壳重启后重新开始计时）。
type Settle struct {
	Size    int64
	MtimeNS int64
	// Since 是大小和修改时间最后一次变化（或开始观察）的时间。
	Since time.Time
}

// Probe 是判定时才去做的两项检查（只在大小和修改时间稳定 2 秒以后做，免得扫描软件写的时候反复打开文件）。
type Probe interface {
	// Exclusive：能以独占方式打开（Windows）。
	Exclusive() bool
	// Tail：文件尾完整。读不了按不完整算。
	Tail() bool
}

// Judge 按这次看到的大小和修改时间推进观察记录并给出结论。s 为 nil 表示第一次看到（开始计时）。
// 返回新的记录（调用方存起来下次再传进来）。
func Judge(s *Settle, size, mtimeNS int64, now time.Time, probe Probe) (*Settle, Verdict) {
	if s == nil || s.Size != size || s.MtimeNS != mtimeNS {
		return &Settle{Size: size, MtimeNS: mtimeNS, Since: now}, KeepWaiting
	}
	quiet := now.Sub(s.Since)
	if quiet < SettleQuiet {
		return s, KeepWaiting
	}
	if !probe.Exclusive() {
		if quiet >= SettleGiveUp {
			return s, Locked
		}
		return s, KeepWaiting
	}
	if probe.Tail() {
		return s, Ready
	}
	if quiet >= SettleGiveUp {
		return s, Incomplete
	}
	return s, KeepWaiting
}

// fileProbe 是 [Probe] 对本机文件的实现。
type fileProbe string

func (p fileProbe) Exclusive() bool { return canOpenExclusive(string(p)) }

func (p fileProbe) Tail() bool {
	ok, err := TailCompleteFile(string(p))
	return err == nil && ok
}
