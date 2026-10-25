package file

import (
	"bytes"
	"math"

	"doclite/server/internal/platform/imagehdr"
)

// CoverLongEdge 是封面的长边（像素）：客户端按原图转正后的方向缩到长边 400（原图更小时不放大）。
const CoverLongEdge = 400

// ValidateCover 校验客户端生成的封面（requirements 5.6"封面"，用户决定 2026-09-28）。server 只读封面的文件头，
// 不解码（硬性规则 11）：
//   - ≤ 256 KiB；是 JPEG、读得出 SOF 的宽高；以 EOI（FF D9）结尾；
//   - 不带 EXIF 方向：方向 2–8 拒绝（封面按转正后的方向生成，带了方向浏览器会再转一次）；
//   - 长边 ≤ 400，也不超过原图（转正后）的长边；
//   - 横竖和原图一致（任何一方是正方形时不查）；
//   - 长宽比一致：期望的短边 = round(原图短边 × 封面长边 ÷ 原图长边)，实际短边和它相差不超过 1 像素。
//
// 不合规返回 400 invalid_cover（message 写明是哪一条）；这时还什么都没写，客户端去掉封面重传。
func ValidateCover(data []byte, original imagehdr.Info) error {
	if len(data) > MaxCoverBytes {
		return coverError("超过 %s", HumanBytes(MaxCoverBytes))
	}
	info, err := imagehdr.Parse(bytes.NewReader(data), int64(len(data)), 0)
	if err != nil {
		return coverError("读不出 JPEG 的文件头（%s）", err.Error())
	}
	if info.Format != imagehdr.JPEG {
		return coverError("要是 JPEG，实际是 %s", info.Format)
	}
	if len(data) < 2 || data[len(data)-2] != 0xFF || data[len(data)-1] != 0xD9 {
		return coverError("JPEG 不完整（不以 FF D9 结尾）")
	}
	if info.Orientation != 1 {
		return coverError("不能带 EXIF 方向（方向是 %d），要按转正后的方向生成", info.Orientation)
	}
	cw, ch := info.Width, info.Height
	ow, oh := original.Width, original.Height
	cLong, cShort := max(cw, ch), min(cw, ch)
	oLong, oShort := max(ow, oh), min(ow, oh)
	if cLong > CoverLongEdge {
		return coverError("长边 %d 像素，超过 %d", cLong, CoverLongEdge)
	}
	if cLong > oLong {
		return coverError("%d×%d 比原图 %d×%d 还大", cw, ch, ow, oh)
	}
	if cw != ch && ow != oh && (cw > ch) != (ow > oh) {
		return coverError("横竖和原图不一致（封面 %d×%d，原图转正后 %d×%d）", cw, ch, ow, oh)
	}
	want := math.Round(float64(oShort) * float64(cLong) / float64(oLong))
	if math.Abs(float64(cShort)-want) > 1 {
		return coverError("长宽比和原图不一致（封面 %d×%d，原图转正后 %d×%d，短边应为 %d 左右）", cw, ch, ow, oh, int(want))
	}
	return nil
}
