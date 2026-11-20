//go:build windows

package platform

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
	shell32                        = windows.NewLazySystemDLL("shell32.dll")
	procDragQueryFileW             = shell32.NewProc("DragQueryFileW")
)

const (
	cfDIB   = 8
	cfHDROP = 15
)

func clipboardImage() ([]byte, []string, error) {
	// 剪贴板的打开状态属于线程，打开、读取、关闭必须在同一个系统线程上。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return nil, nil, err
	}
	defer procCloseClipboard.Call()

	// 浏览器、截图工具会同时放一份 PNG（保留透明通道、不用自己解析），优先用它。
	name, _ := syscall.UTF16PtrFromString("PNG")
	if f, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name))); f != 0 && available(f) {
		if b, err := globalBytes(f); err == nil && len(b) > 0 {
			return b, nil, nil
		}
	}
	if available(cfDIB) {
		b, err := globalBytes(cfDIB)
		if err != nil {
			return nil, nil, err
		}
		img, err := decodeDIB(b)
		if err != nil {
			return nil, nil, err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, nil, err
		}
		return buf.Bytes(), nil, nil
	}
	if available(cfHDROP) {
		return nil, dropFiles(), nil
	}
	return nil, nil, nil
}

// openClipboard 打开剪贴板。别的程序正占用时会失败，稍等重试几次。
func openClipboard() error {
	for range 10 {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("剪贴板正被其他程序占用，请稍后再试")
}

func available(format uintptr) bool {
	r, _, _ := procIsClipboardFormatAvailable.Call(format)
	return r != 0
}

// globalBytes 复制出剪贴板里某个格式的全部字节。句柄归剪贴板所有，不能释放。
func globalBytes(format uintptr) ([]byte, error) {
	h, _, _ := procGetClipboardData.Call(format)
	if h == 0 {
		return nil, errors.New("读取剪贴板失败")
	}
	size, _, _ := procGlobalSize.Call(h)
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 || size == 0 {
		return nil, errors.New("读取剪贴板失败")
	}
	defer procGlobalUnlock.Call(h)
	// GlobalLock 返回的是系统分配的内存地址，不归 Go 的垃圾回收管，用 unsafe.Add 从 nil 偏移得到指针。
	return bytes.Clone(unsafe.Slice((*byte)(unsafe.Add(nil, p)), size)), nil
}

// dropFiles 取出资源管理器里复制的文件路径。
func dropFiles() []string {
	h, _, _ := procGetClipboardData.Call(cfHDROP)
	if h == 0 {
		return nil
	}
	n, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	var out []string
	for i := range n {
		l, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		if l == 0 {
			continue
		}
		buf := make([]uint16, l+1)
		procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		out = append(out, windows.UTF16ToString(buf))
	}
	return out
}

// decodeDIB 把 CF_DIB（BITMAPINFOHEADER 加像素）转成图片。只支持截图和复制图片时常见的
// 24 / 32 位不压缩格式；32 位的第四个字节在多数程序里没有意义（全是 0），这时按不透明处理。
func decodeDIB(b []byte) (image.Image, error) {
	if len(b) < 40 {
		return nil, errors.New("剪贴板里的位图格式不对")
	}
	le := binary.LittleEndian
	hdr := int(le.Uint32(b[0:]))
	w, h := int(int32(le.Uint32(b[4:]))), int(int32(le.Uint32(b[8:])))
	bpp, comp := int(le.Uint16(b[14:])), le.Uint32(b[16:])
	topDown := h < 0
	if topDown {
		h = -h
	}
	const biRGB, biBitfields = 0, 3
	if w <= 0 || h <= 0 || w*h > 80_000_000 || (bpp != 24 && bpp != 32) || (comp != biRGB && comp != biBitfields) || hdr < 40 {
		return nil, fmt.Errorf("不支持剪贴板里的这种位图（%d 位，压缩方式 %d）", bpp, comp)
	}
	off := hdr
	if comp == biBitfields && hdr == 40 { // 掩码跟在 40 字节的头后面；V4 / V5 头里已经含了掩码
		off += 12
	}
	if clrUsed := int(le.Uint32(b[32:])); clrUsed > 0 { // 24 / 32 位一般没有调色板，有的话跳过
		off += clrUsed * 4
	}
	stride := (w*bpp + 31) / 32 * 4
	if off+stride*h > len(b) {
		return nil, errors.New("剪贴板里的位图数据不完整")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	step := bpp / 8
	anyAlpha := false
	for y := range h {
		row := b[off+stride*y:]
		dy := h - 1 - y // 默认自下而上存储
		if topDown {
			dy = y
		}
		for x := range w {
			px := row[x*step:]
			i := img.PixOffset(x, dy)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = px[2], px[1], px[0], 255
			if step == 4 {
				img.Pix[i+3] = px[3]
				anyAlpha = anyAlpha || px[3] != 0
			}
		}
	}
	if step == 4 && !anyAlpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	return img, nil
}
