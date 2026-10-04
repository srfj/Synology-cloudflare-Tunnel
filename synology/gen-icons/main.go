// Command gen-icons renders the package icons (icon_16.png .. icon_256.png)
// used by the DSM desktop shortcut. Standard library only.
//
//	go run ./synology/gen-icons <output-dir>
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

var sizes = []int{16, 24, 32, 48, 64, 72, 256}

func main() {
	if len(os.Args) < 2 {
		panic("usage: gen-icons <output-dir>")
	}
	out := os.Args[1]
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}
	for _, s := range sizes {
		f, err := os.Create(filepath.Join(out, "icon_"+strconv.Itoa(s)+".png"))
		if err != nil {
			panic(err)
		}
		if err := png.Encode(f, render(s)); err != nil {
			panic(err)
		}
		_ = f.Close()
	}
}

func inRoundRect(u, v, x0, y0, x1, y1, r float64) bool {
	cx := math.Min(math.Max(u, x0+r), x1-r)
	cy := math.Min(math.Max(v, y0+r), y1-r)
	dx, dy := u-cx, v-cy
	return dx*dx+dy*dy <= r*r
}

func cloud(u, v float64) bool {
	if inRoundRect(u, v, 0.355, 0.520, 0.645, 0.725, 0.150) {
		return true
	}
	circles := [3][3]float64{
		{0.355, 0.545, 0.150},
		{0.500, 0.450, 0.180},
		{0.645, 0.545, 0.150},
	}
	for _, c := range circles {
		dx, dy := u-c[0], v-c[1]
		if dx*dx+dy*dy <= c[2]*c[2] {
			return true
		}
	}
	return false
}

// sample returns the premultiplied colour at normalised coordinates (u, v).
func sample(u, v float64) (float64, float64, float64, float64) {
	if !inRoundRect(u, v, 0, 0, 1, 1, 0.22) {
		return 0, 0, 0, 0
	}
	r, g, b := 246/255.0, 130/255.0, 31/255.0
	if cloud(u, v) {
		r, g, b = 1, 1, 1
	}
	return r, g, b, 1
}

func render(size int) *image.NRGBA {
	const ss = 4
	n := size * ss
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var pr, pg, pb, pa float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(px*ss+sx) + 0.5) / float64(n)
					v := (float64(py*ss+sy) + 0.5) / float64(n)
					r, g, b, a := sample(u, v)
					pr += r * a
					pg += g * a
					pb += b * a
					pa += a
				}
			}
			if pa == 0 {
				img.SetNRGBA(px, py, color.NRGBA{})
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(math.Round(pr / pa * 255)),
				G: uint8(math.Round(pg / pa * 255)),
				B: uint8(math.Round(pb / pa * 255)),
				A: uint8(math.Round(pa / (ss * ss) * 255)),
			})
		}
	}
	return img
}
