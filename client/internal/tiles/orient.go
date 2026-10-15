package tiles

// 方向（EXIF / TIFF 的 Orientation，1–8；shared/imagehdr 读出，PNG 一律 1）：文件里存的像素 (x, y)（存的宽高 w×h）
// 转正后落在哪里。5–8 转正后宽高交换。和 server（imagehdr 交换宽高）、封面（requirements 5.6）的规则一致：
//
//	1 原样            (x, y)
//	2 水平翻转        (w−1−x, y)
//	3 转 180°         (w−1−x, h−1−y)
//	4 垂直翻转        (x, h−1−y)
//	5 转置            (y, x)
//	6 顺时针转 90°    (h−1−y, x)
//	7 反转置          (h−1−y, w−1−x)
//	8 逆时针转 90°    (y, w−1−x)

// OrientedSize 是转正后的宽高。
func OrientedSize(orientation, w, h int) (int, int) {
	if orientation >= 5 && orientation <= 8 {
		return h, w
	}
	return w, h
}

// orientPoint 把存的像素 (x, y) 换到转正后的位置。不认识的方向按 1。
func orientPoint(orientation, w, h, x, y int) (int, int) {
	switch orientation {
	case 2:
		return w - 1 - x, y
	case 3:
		return w - 1 - x, h - 1 - y
	case 4:
		return x, h - 1 - y
	case 5:
		return y, x
	case 6:
		return h - 1 - y, x
	case 7:
		return h - 1 - y, w - 1 - x
	case 8:
		return y, w - 1 - x
	}
	return x, y
}

// rowPlacement：存的第 y 行转正后第一个像素的下标和逐像素的步长（以像素计，转正后的图宽 ow）。
// 每行内的映射对 x 是线性的，所以只要算 x=0、x=1 两个点。
func rowPlacement(orientation, w, h, ow, y int) (start, step int) {
	x0, y0 := orientPoint(orientation, w, h, 0, y)
	start = y0*ow + x0
	if w == 1 {
		return start, 0
	}
	x1, y1 := orientPoint(orientation, w, h, 1, y)
	return start, (y1*ow + x1) - start
}
