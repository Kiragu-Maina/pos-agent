package printer

import (
	"bytes"
	"errors"
	"image"
	// Register the decoders we accept. Both are pure Go (no cgo).
	_ "image/jpeg"
	_ "image/png"
)

// Printed logo size caps. Kept moderate so the logo never spans the full paper
// and stays crisp on a low-resolution thermal head.
const (
	logoMaxWidthDots  = 384
	logoMaxHeightDots = 320
)

// paperDots returns the printable dot width for a paper size.
func paperDots(mm int) int {
	if mm == 58 {
		return 384
	}
	return 576
}

// rasterFromImage decodes an image and returns ESC/POS "GS v 0" raster bytes
// that print it as a monochrome bitmap. It scales the image down to fit the dot
// limits, converts to 1-bit with a luminance threshold (transparent pixels are
// treated as white), and packs the bits MSB first.
func rasterFromImage(data []byte, maxWidthDots int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, errors.New("empty image")
	}
	if maxWidthDots <= 0 || maxWidthDots > logoMaxWidthDots {
		maxWidthDots = logoMaxWidthDots
	}

	// Target width: cap to the limit and round down to a multiple of 8, since
	// each printed row is byte aligned.
	tw := srcW
	if tw > maxWidthDots {
		tw = maxWidthDots
	}
	tw &^= 7
	if tw < 8 {
		tw = 8
	}
	th := srcH * tw / srcW
	if th > logoMaxHeightDots {
		th = logoMaxHeightDots
		tw = (srcW * th / srcH) &^ 7
		if tw < 8 {
			tw = 8
		}
	}
	if th < 1 {
		th = 1
	}

	bytesPerRow := tw / 8
	bits := make([]byte, bytesPerRow*th)
	for y := 0; y < th; y++ {
		sy := b.Min.Y + y*srcH/th
		for x := 0; x < tw; x++ {
			sx := b.Min.X + x*srcW/tw
			r, g, bl, a := img.At(sx, sy).RGBA() // 16-bit per channel
			if a < 0x8000 {
				continue // transparent -> no ink
			}
			lum := (299*r + 587*g + 114*bl) / 1000
			if lum < 0x8000 { // dark -> ink
				bits[y*bytesPerRow+x/8] |= 0x80 >> (uint(x) % 8)
			}
		}
	}

	var out bytes.Buffer
	out.Write([]byte{0x1D, 0x76, 0x30, 0x00}) // GS v 0, mode 0 (normal)
	out.WriteByte(byte(bytesPerRow & 0xFF))   // xL
	out.WriteByte(byte(bytesPerRow >> 8))     // xH
	out.WriteByte(byte(th & 0xFF))            // yL
	out.WriteByte(byte(th >> 8))              // yH
	out.Write(bits)
	return out.Bytes(), nil
}

// ValidImage reports whether data decodes as a supported image. Used to reject
// junk uploads before storing them.
func ValidImage(data []byte) bool {
	_, _, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil
}
