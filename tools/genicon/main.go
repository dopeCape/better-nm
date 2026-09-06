// Command genicon renders the bnm application icon (256x256 PNG) with the standard
// library only, so the repo needs no binary asset checked in by hand.
//
//	go run ./tools/genicon packaging/desktop/bnm.png
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
	out := "packaging/desktop/bnm.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	const n = 256
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	bg := color.NRGBA{0x12, 0x1a, 0x2b, 0xff}
	fg := color.NRGBA{0x4f, 0xd1, 0xc5, 0xff}
	fg2 := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	cx, cy := float64(n)/2, float64(n)*0.72
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			// rounded square background
			px, py := float64(x)+0.5, float64(y)+0.5
			r := 48.0
			ix := math.Max(math.Abs(px-n/2)-(n/2-r), 0)
			iy := math.Max(math.Abs(py-n/2)-(n/2-r), 0)
			if math.Hypot(ix, iy) > r {
				continue
			}
			img.SetNRGBA(x, y, bg)
			// three signal arcs, top-right quadrant fan, plus a dot
			d := math.Hypot(px-cx, py-cy)
			ang := math.Atan2(cy-py, px-cx) // 0 = right, pi/2 = up
			inFan := ang > math.Pi/4 && ang < 3*math.Pi/4
			for i, radius := range []float64{150, 110, 70} {
				if inFan && math.Abs(d-radius) < 13 {
					c := fg
					if i == 1 {
						c = fg2
					}
					img.SetNRGBA(x, y, c)
				}
			}
			if d < 22 {
				img.SetNRGBA(x, y, fg)
			}
		}
	}
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
