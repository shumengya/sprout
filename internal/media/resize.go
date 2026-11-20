package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // 注册 gif 解码器
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // 注册 webp 解码器：超限的 webp 也能缩放，内核估算 token 时也能读出尺寸
)

// fit 返回发给模型的图片字节与类型。大小和长边都在上限内时原样返回。
func fit(raw []byte, mt string) ([]byte, string, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		// 解不出尺寸（少见的编码变体）：在大小上限内就原样发，交给服务端判断；超限的没法压缩。
		if len(raw) <= MaxImageBytes {
			return raw, mt, nil
		}
		return nil, "", fmt.Errorf("有 %s，超过上限 %s，而且无法解码来压缩", Size(len(raw)), Size(MaxImageBytes))
	}
	long := max(cfg.Width, cfg.Height)
	if len(raw) <= MaxImageBytes && long <= MaxImageEdge {
		return raw, mt, nil
	}
	if cfg.Width*cfg.Height > maxSourcePixels {
		return nil, "", fmt.Errorf("尺寸 %d×%d 太大，不处理", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("解码失败: %v", err)
	}
	// 手机照片常常横着存、靠 EXIF 标记方向；重新编码会丢掉 EXIF，所以要先把像素转正。
	orient := 1
	if mt == "image/jpeg" {
		orient = jpegOrientation(raw)
	}
	// 截图、图表这类大片纯色加文字的图，png 更清楚，通常也更小；原本就是 jpeg 的照片直接用 jpeg。
	tryPNG := mt != "image/jpeg"
	edge := min(long, MaxImageEdge)
	for range 4 { // 每次长边缩到 3/4：2000 → 1500 → 1125 → 843，最后一档的 jpeg 不会超过上限
		img := orientImage(scaleTo(src, edge), orient)
		if tryPNG {
			if b, err := encodePNG(img); err == nil && len(b) <= MaxImageBytes {
				return b, "image/png", nil
			}
		}
		for _, q := range []int{85, 70} {
			if b, err := encodeJPEG(img, q); err == nil && len(b) <= MaxImageBytes {
				return b, "image/jpeg", nil
			}
		}
		edge = edge * 3 / 4
	}
	return nil, "", errors.New("压缩后仍超过上限")
}

// scaleTo 把图片等比缩到长边为 edge，结果总是 *image.RGBA（edge 不小于原长边时只做格式转换）。
func scaleTo(src image.Image, edge int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if long := max(w, h); edge < long {
		w, h = max(w*edge/long, 1), max(h*edge/long, 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		xdraw.Draw(dst, dst.Bounds(), src, b.Min, xdraw.Src)
		return dst
	}
	// CatmullRom 缩小时不会像双线性那样出锯齿和摩尔纹，截图里的小字也更清楚。
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return dst
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

// encodeJPEG 编码成 jpeg。jpeg 没有透明通道，透明部分铺白底，否则会变成黑色。
func encodeJPEG(img *image.RGBA, quality int) ([]byte, error) {
	var src image.Image = img
	if !img.Opaque() {
		bg := image.NewRGBA(img.Bounds())
		xdraw.Draw(bg, bg.Bounds(), image.NewUniform(color.White), image.Point{}, xdraw.Src)
		xdraw.Draw(bg, bg.Bounds(), img, img.Bounds().Min, xdraw.Over)
		src = bg
	}
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality})
	return buf.Bytes(), err
}

// orientImage 按 EXIF 方向（1–8）把像素转正。1 或未知值原样返回。
func orientImage(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 { // 5–8 要转 90°，宽高互换
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			// 由目标像素反推源像素。
			var sx, sy int
			switch o {
			case 2: // 水平翻转
				sx, sy = w-1-x, y
			case 3: // 转 180°
				sx, sy = w-1-x, h-1-y
			case 4: // 垂直翻转
				sx, sy = x, h-1-y
			case 5: // 沿主对角线翻转
				sx, sy = y, x
			case 6: // 顺时针转 90°
				sx, sy = y, h-1-x
			case 7: // 沿副对角线翻转
				sx, sy = w-1-y, h-1-x
			case 8: // 逆时针转 90°
				sx, sy = w-1-y, x
			}
			copy(dst.Pix[dst.PixOffset(x, y):][:4], src.Pix[src.PixOffset(sx, sy):][:4])
		}
	}
	return dst
}

// jpegOrientation 从 jpeg 的 EXIF（APP1 段）里读出方向标记，读不到时返回 1。
// 只认 IFD0 里的 0x0112 这一项，用不着完整的 EXIF 解析。
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		switch {
		case marker == 0xFF: // 填充字节
			i++
			continue
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7): // 没有长度字段的标记
			i += 2
			continue
		case marker == 0xDA || marker == 0xD9: // 到了图像数据，后面没有元数据了
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return 1
		}
		if seg := b[i+4 : i+2+n]; marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

// exifOrientation 解析 TIFF 结构的 EXIF 数据，返回 IFD0 里的方向值。
func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	for k := range int(bo.Uint16(t[off:])) {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			// SHORT 类型的值左对齐放在 4 字节的值字段里。
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
