package bundle

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"doclite/server/internal/platform/imagehdr"
)

// Kind 是错误的类别。[Error.Retryable] 区分要不要重试。
type Kind string

const (
	// 某一页的原图有问题（重试没用，提示重扫或移出后重导）：

	// KindIntegrity：读到的比快照记的长、长度对但 sha256 不一致、文件开头的魔数和 mime 不一致。
	KindIntegrity Kind = "integrity"
	// KindCorrupt：文件头读不下去、偏移越界、条带数据不够、PNG 块的 CRC 不对、转换时解码失败。
	KindCorrupt Kind = "corrupt"
	// KindUnsupported：放不进 PDF、也转换不了的写法（算术编码的 JPEG、分平面的 JPEG 压缩 TIFF……）。
	KindUnsupported Kind = "unsupported"
	// KindTooLarge：转换要的内存超过 Options.ConvertMemoryLimit，或者一件的 PDF 超过 10 GB。
	KindTooLarge Kind = "too_large"

	// 可以重试的：

	// KindSource：读原图失败（包括数据源给的比快照记的短）。
	KindSource Kind = "source"
	// KindTemp：临时文件读写失败（磁盘满之类）。
	KindTemp Kind = "temp"
	// KindOutput：写 writer 失败。
	KindOutput Kind = "output"

	// 其他：

	// KindCanceled：ctx 取消了。errors.Is(err, context.Canceled) 也成立。
	KindCanceled Kind = "canceled"
	// KindInvalidInput：调用方给的 Job 不对（没档号、没页、页号不是严格递增、mime 或状态不认识……）。
	KindInvalidInput Kind = "invalid_input"
)

// Error 是 [Generate] 和 [Inspect] 返回的错误。
//
// 页的问题（Integrity、Corrupt、Unsupported、TooLarge）和读这一页失败（Source）时，ArchiveNo、PageNo、FileID、
// SnapshotID 指出是哪一页，Generate 返回的还有 ItemIndex、PageIndex；其他类别不一定有（下标为 -1、页号为 0）。
type Error struct {
	Kind Kind

	ArchiveNo  string
	PageNo     int
	FileID     uuid.UUID
	SnapshotID uuid.UUID
	// ItemIndex、PageIndex 是在 Job.Items、Item.Pages 里的下标，-1 表示不针对某一件、某一页。
	ItemIndex, PageIndex int

	// Reason 是给用户看的原因（中文），不含档号和页号。
	Reason string
	// Err 是底层的错误，可能为 nil。
	Err error
}

func (e *Error) Error() string {
	switch {
	case e.PageNo > 0:
		return fmt.Sprintf("%s 第 %d 页：%s", e.ArchiveNo, e.PageNo, e.Reason)
	case e.ArchiveNo != "":
		return fmt.Sprintf("%s：%s", e.ArchiveNo, e.Reason)
	}
	return e.Reason
}

func (e *Error) Unwrap() error { return e.Err }

// Retryable：读原图、临时文件、写 writer 失败可以重试；页的问题、取消、输入不对不重试。
func (e *Error) Retryable() bool {
	switch e.Kind {
	case KindSource, KindTemp, KindOutput:
		return true
	}
	return false
}

// PageProblem：是不是某一页的原图有问题（要提示重扫或移出）。
func (e *Error) PageProblem() bool {
	switch e.Kind {
	case KindIntegrity, KindCorrupt, KindUnsupported, KindTooLarge:
		return true
	}
	return false
}

// KindOf 返回 err 的类别；不是 *Error 时返回空串。
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// IsRetryable：err 是可以重试的 *Error。
func IsRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable()
}

// ---------------------------------------------------------------------------
// 包内的错误：处理过程中用这几种包一层，最后由 classify 定类别、补上是哪一页。

// problem 是原图本身的问题（Integrity、Corrupt、Unsupported、TooLarge）。
type problem struct {
	kind   Kind
	reason string
	err    error
}

func (p *problem) Error() string { return p.reason }
func (p *problem) Unwrap() error { return p.err }

func corruptf(format string, args ...any) error {
	return &problem{kind: KindCorrupt, reason: fmt.Sprintf(format, args...)}
}

func unsupportedf(format string, args ...any) error {
	return &problem{kind: KindUnsupported, reason: fmt.Sprintf(format, args...)}
}

func tooLargef(format string, args ...any) error {
	return &problem{kind: KindTooLarge, reason: fmt.Sprintf(format, args...)}
}

func integrityf(format string, args ...any) error {
	return &problem{kind: KindIntegrity, reason: fmt.Sprintf(format, args...)}
}

// ioError 是读原图、临时文件、写 writer 失败。
type ioError struct {
	kind Kind // KindSource、KindTemp、KindOutput
	op   string
	err  error
}

func (e *ioError) Error() string { return e.op + "：" + e.err.Error() }
func (e *ioError) Unwrap() error { return e.err }

func sourceErr(op string, err error) error { return &ioError{kind: KindSource, op: op, err: err} }
func tempErr(op string, err error) error   { return &ioError{kind: KindTemp, op: op, err: err} }
func outputErr(err error) error {
	return &ioError{kind: KindOutput, op: "写导出物失败", err: err}
}

func invalidInput(format string, args ...any) *Error {
	return &Error{Kind: KindInvalidInput, ItemIndex: -1, PageIndex: -1, Reason: fmt.Sprintf(format, args...)}
}

// classify 把处理过程中的错误变成 *Error。ctx 已经取消时一律是 KindCanceled（读写中途被取消，底层报的错是表象）。
// 不认识的错误按 fallback 算：处理某一页时是 KindCorrupt（解码器报的错），其他时候是 KindTemp。
func classify(ctx context.Context, err error, fallback Kind) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	out := &Error{ItemIndex: -1, PageIndex: -1, Err: err}
	if ctxErr := ctx.Err(); ctxErr != nil {
		out.Kind, out.Reason = KindCanceled, "导出已取消"
		if !errors.Is(err, ctxErr) {
			out.Err = errors.Join(ctxErr, err)
		}
		return out
	}
	var p *problem
	var ie *ioError
	var hdr *imagehdr.Error
	switch {
	case errors.As(err, &ie):
		out.Kind, out.Reason = ie.kind, ie.Error()
	case errors.As(err, &p):
		out.Kind, out.Reason = p.kind, p.reason
	case errors.As(err, &hdr):
		out.Reason = hdr.Error()
		switch {
		case errors.Is(err, imagehdr.ErrUnsupported), errors.Is(err, imagehdr.ErrMultiPage):
			out.Kind = KindUnsupported
		case errors.Is(err, imagehdr.ErrTooLarge):
			out.Kind = KindTooLarge
		default:
			out.Kind = KindCorrupt
		}
	default:
		out.Kind, out.Reason = fallback, err.Error()
		if fallback == KindCorrupt {
			out.Reason = "图片解不开：" + err.Error()
		}
	}
	return out
}

// atPage 补上是哪一件、哪一页（page 为 nil 时只有件）。下标不知道时传 -1。
func (e *Error) atPage(itemIdx int, item *Item, pageIdx int, page *Page) *Error {
	e.ItemIndex, e.ArchiveNo = itemIdx, item.ArchiveNo
	if page != nil {
		e.PageIndex, e.PageNo, e.FileID, e.SnapshotID = pageIdx, page.PageNo, page.FileID, page.SnapshotID
	}
	return e
}
