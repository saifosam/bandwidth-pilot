//go:build windows

package engine

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	user32             = windows.NewLazySystemDLL("user32.dll")
	gdi32              = windows.NewLazySystemDLL("gdi32.dll")
	procSHGetFileInfoW = shell32.NewProc("SHGetFileInfoW")
	procGetIconInfo    = user32.NewProc("GetIconInfo")
	procDestroyIcon    = user32.NewProc("DestroyIcon")
	procGetDC          = user32.NewProc("GetDC")
	procReleaseDC      = user32.NewProc("ReleaseDC")
	procGetObjectW     = gdi32.NewProc("GetObjectW")
	procGetDIBits      = gdi32.NewProc("GetDIBits")
	procDeleteObject   = gdi32.NewProc("DeleteObject")
)

type shFileInfo struct {
	hIcon       uintptr
	iIcon       int32
	attributes  uint32
	displayName [260]uint16
	typeName    [80]uint16
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

type gdiBitmap struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

type iconCache struct {
	mu sync.Mutex
	m  map[string]string
}

func newIconCache() *iconCache { return &iconCache{m: map[string]string{}} }

// get returns a PNG data URL of the file's shell icon ("" if unavailable).
func (c *iconCache) get(path string) string {
	if path == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[path]; ok {
		return v
	}
	v := ""
	if b := extractIconPNG(path); b != nil {
		v = "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)
	}
	c.m[path] = v
	return v
}

func extractIconPNG(path string) []byte {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	var sfi shFileInfo
	const shgfiIcon = 0x100 // large icon
	r, _, _ := procSHGetFileInfoW.Call(uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&sfi)), unsafe.Sizeof(sfi), shgfiIcon)
	if r == 0 || sfi.hIcon == 0 {
		return nil
	}
	defer procDestroyIcon.Call(sfi.hIcon)

	var ii iconInfo
	if r, _, _ := procGetIconInfo.Call(sfi.hIcon, uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil
	}
	if ii.hbmMask != 0 {
		defer procDeleteObject.Call(ii.hbmMask)
	}
	if ii.hbmColor == 0 {
		return nil
	}
	defer procDeleteObject.Call(ii.hbmColor)

	var bm gdiBitmap
	if r, _, _ := procGetObjectW.Call(ii.hbmColor, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return nil
	}
	w, h := int(bm.bmWidth), int(bm.bmHeight)
	if w <= 0 || h <= 0 || w > 512 || h > 512 {
		return nil
	}
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return nil
	}
	defer procReleaseDC.Call(0, hdc)

	bih := bitmapInfoHeader{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	bih.size = uint32(unsafe.Sizeof(bih))
	pix := make([]byte, w*h*4)
	if r, _, _ := procGetDIBits.Call(hdc, ii.hbmColor, 0, uintptr(h),
		uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bih)), 0); r == 0 {
		return nil
	}
	// BGRA -> RGBA. Icons without an alpha channel come back fully transparent.
	anyAlpha := false
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+2] = pix[i+2], pix[i]
		if pix[i+3] != 0 {
			anyAlpha = true
		}
	}
	if !anyAlpha {
		for i := 3; i < len(pix); i += 4 {
			pix[i] = 255
		}
	}
	img := &image.NRGBA{Pix: pix, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
