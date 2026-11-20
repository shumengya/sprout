//go:build windows

package platform

import (
	"encoding/binary"
	"image/color"
	"testing"
)

// dib 构造一个 CF_DIB：40 字节的 BITMAPINFOHEADER 加像素。h 为负表示自上而下存储。
func dib(w, h, bpp int, pixels []byte) []byte {
	b := make([]byte, 40)
	le := binary.LittleEndian
	le.PutUint32(b[0:], 40)
	le.PutUint32(b[4:], uint32(int32(w)))
	le.PutUint32(b[8:], uint32(int32(h)))
	le.PutUint16(b[12:], 1)
	le.PutUint16(b[14:], uint16(bpp))
	return append(b, pixels...)
}

func TestDecodeDIB24BottomUp(t *testing.T) {
	// 2×2，每行 6 字节补齐到 8。自下而上：第一行数据是图片的最后一行。
	px := []byte{
		0, 0, 255, 0, 255, 0, 0, 0, // 底行：红、绿（BGR）
		255, 0, 0, 255, 255, 255, 0, 0, // 顶行：蓝、白
	}
	img, err := decodeDIB(dib(2, 2, 24, px))
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]int]color.NRGBA{
		{0, 0}: {0, 0, 255, 255}, {1, 0}: {255, 255, 255, 255},
		{0, 1}: {255, 0, 0, 255}, {1, 1}: {0, 255, 0, 255},
	}
	for p, c := range want {
		if got := img.At(p[0], p[1]); got != c {
			t.Errorf("(%d,%d) = %v，期望 %v", p[0], p[1], got, c)
		}
	}
}

// 32 位、alpha 全为 0（多数程序不填这个字节）时按不透明处理，否则整张图是透明的。
func TestDecodeDIB32ZeroAlphaIsOpaque(t *testing.T) {
	img, err := decodeDIB(dib(1, -1, 32, []byte{10, 20, 30, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.At(0, 0); got != (color.NRGBA{30, 20, 10, 255}) {
		t.Errorf("像素 = %v", got)
	}
}

func TestDecodeDIBRejectsUnsupported(t *testing.T) {
	if _, err := decodeDIB(dib(1, 1, 8, []byte{0, 0, 0, 0})); err == nil {
		t.Error("8 位调色板位图应报不支持")
	}
	if _, err := decodeDIB(dib(4, 4, 24, []byte{1, 2, 3})); err == nil {
		t.Error("数据不完整应报错")
	}
}
