// genicon draws the application icon (the favicon of the web UI: a blue rounded square with a play triangle) and
// writes cmd/medialib/winres/icon.ico plus the PNGs the window uses. Run it from the repository root:
//
//	go run ./scripts/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

const ss = 4 // samples per pixel side

// shape coverage in the 32x32 design space of the favicon
func inside(x, y float64) (bool, bool) {
	// rounded square: x,y in [2,30]x[3,29], radius 8
	const l, t, r, b, rad = 2.0, 3.0, 30.0, 29.0, 8.0
	in := x >= l && x <= r && y >= t && y <= b
	if in {
		cx, cy := x, y
		if x < l+rad {
			cx = l + rad
		} else if x > r-rad {
			cx = r - rad
		}
		if y < t+rad {
			cy = t + rad
		} else if y > b-rad {
			cy = b - rad
		}
		dx, dy := x-cx, y-cy
		in = dx*dx+dy*dy <= rad*rad
	}
	// play triangle (12,9.5) (12,22.5) (23,16)
	tri := false
	if x >= 12 && x <= 23 {
		half := 6.5 * (23 - x) / 11
		tri = y >= 16-half && y <= 16+half
	}
	return in, tri
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 32.0 / float64(size)
	for py := 0; py < size; py++ {
		for pxl := 0; pxl < size; pxl++ {
			var a, tri float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(pxl) + (float64(sx)+0.5)/ss) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss) * scale
					in, t := inside(x, y)
					if in {
						a++
						if t {
							tri++
						}
					}
				}
			}
			n := float64(ss * ss)
			if a == 0 {
				continue
			}
			// a soft top-to-bottom gradient on the blue, white where the triangle is
			g := float64(py) / float64(size)
			base := [3]float64{0x36 - 14*g, 0x7c - 22*g, 0xe8 - 20*g}
			k := tri / a
			r := base[0]*(1-k) + 255*k
			gr := base[1]*(1-k) + 255*k
			bl := base[2]*(1-k) + 255*k
			img.SetNRGBA(pxl, py, color.NRGBA{uint8(r), uint8(gr), uint8(bl), uint8(255 * a / n)})
		}
	}
	return img
}

func encode(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		log.Fatal(err)
	}
	return b.Bytes()
}

func main() {
	dir := filepath.Join("cmd", "medialib", "winres")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	pngs := make([][]byte, len(sizes))
	for i, s := range sizes {
		pngs[i] = encode(render(s))
		if s == 32 || s == 256 {
			name := filepath.Join(dir, "icon-"+itoa(s)+".png")
			if err := os.WriteFile(name, pngs[i], 0o644); err != nil {
				log.Fatal(err)
			}
		}
	}
	// ICO with PNG-compressed images (Windows Vista and later)
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, s := range sizes {
		d := byte(s)
		if s == 256 {
			d = 0
		}
		ico.Write([]byte{d, d, 0, 0})
		binary.Write(&ico, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&ico, binary.LittleEndian, [2]uint32{uint32(len(pngs[i])), uint32(off)})
		off += len(pngs[i])
	}
	for _, p := range pngs {
		ico.Write(p)
	}
	if err := os.WriteFile(filepath.Join(dir, "icon.ico"), ico.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Println("wrote", dir)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}
