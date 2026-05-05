package headless

// PNG manipulation helpers used by the screenshot tools. Lives in
// the headless package so both the CLI (cmd/social-fetch/screenshot.go)
// and the MCP tools (internal/mcp/screenshot_tools.go) can share
// one implementation — they both already import this package, so
// no new dependency edges are added.
//
// PNG decode/encode round-trip uses Go stdlib only (no native
// deps), trading a bit of throughput for portability — typical
// 1MB PNG decodes in < 50 ms on modern hardware.

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
)

// PNGDims reads width + height from a PNG header without doing a
// full decode. PNG IHDR chunk is at fixed offset 8: bytes 8-11 are
// IHDR length, 12-15 are "IHDR" magic, 16-19 are width
// (big-endian uint32), 20-23 are height. Refuses anything that
// isn't a PNG up front so a misrouted JSON / HTML body returns a
// clean error instead of random integers.
func PNGDims(src []byte) (w, h int, err error) {
	if len(src) < 24 || string(src[:8]) != "\x89PNG\r\n\x1a\n" {
		return 0, 0, fmt.Errorf("not a PNG (magic mismatch)")
	}
	if string(src[12:16]) != "IHDR" {
		return 0, 0, fmt.Errorf("PNG IHDR chunk missing")
	}
	w = int(uint32(src[16])<<24 | uint32(src[17])<<16 | uint32(src[18])<<8 | uint32(src[19]))
	h = int(uint32(src[20])<<24 | uint32(src[21])<<16 | uint32(src[22])<<8 | uint32(src[23]))
	return w, h, nil
}

// CropPNGTop returns a PNG of the top maxHeight pixels of src.
// Returns the original bytes unchanged + didCrop=false when the
// image is already shorter than maxHeight (skips the
// decode/encode round-trip in the common case).
func CropPNGTop(src []byte, maxHeight int) (out []byte, didCrop bool, err error) {
	return CropPNGSlice(src, 0, maxHeight)
}

// CropPNGSlice returns a vertical slice of src starting at
// offsetY, up to maxHeight tall (clamped to the actual image
// height). Used by social_fetch_read_screenshot to paginate down
// a tall page in steps — agent calls with offset_y=0, then 4096,
// then 8192, etc.
//
// When offsetY=0 and maxHeight covers the whole image, returns
// src unchanged (didCrop=false) so the no-op case skips the
// PNG round-trip cost.
func CropPNGSlice(src []byte, offsetY, maxHeight int) (out []byte, didCrop bool, err error) {
	if maxHeight <= 0 && offsetY <= 0 {
		return src, false, nil
	}
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return src, false, err
	}
	bounds := img.Bounds()
	if offsetY < 0 {
		offsetY = 0
	}
	if offsetY >= bounds.Dy() {
		return nil, false, fmt.Errorf("offset_y %d beyond image height %d", offsetY, bounds.Dy())
	}
	endY := bounds.Min.Y + offsetY + maxHeight
	if maxHeight <= 0 || endY > bounds.Max.Y {
		endY = bounds.Max.Y
	}
	startY := bounds.Min.Y + offsetY
	if offsetY == 0 && endY == bounds.Max.Y {
		return src, false, nil
	}
	subImager, ok := img.(interface {
		SubImage(r image.Rectangle) image.Image
	})
	if !ok {
		// Defensive: stdlib image decoders all implement
		// SubImage; this guards against a future format that
		// doesn't.
		return src, false, fmt.Errorf("decoded image type does not support cropping")
	}
	cropped := subImager.SubImage(image.Rect(
		bounds.Min.X, startY,
		bounds.Max.X, endY,
	))
	var buf bytes.Buffer
	if err := png.Encode(&buf, cropped); err != nil {
		return src, false, err
	}
	return buf.Bytes(), true, nil
}
