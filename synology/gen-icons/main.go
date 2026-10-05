// Command gen-icons renders the Cloudflare logo used both for the DSM desktop
// shortcut (icon_16.png .. icon_256.png) and for Package Center
// (PACKAGE_ICON.PNG / PACKAGE_ICON_256.PNG).
//
//	go run ./synology/gen-icons <icons-dir> [package-dir]
//
// The logo geometry is the official Cloudflare cloud path from simple-icons; it
// is rasterised with the standard library only so the build has no extra
// dependencies.
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

// cloudflarePath is the "cloudflare" icon path from simple-icons (24x24 viewBox,
// the Cloudflare cloud mark). Do not reformat: the tokenizer below relies on the
// compact SVG number syntax.
const cloudflarePath = "M16.5088 16.8447c.1475-.5068.0908-.9707-.1553-1.3154-.2246-.3164-.6045-.499-1.0615-.5205l-8.6592-.1123a.1559.1559 0 0 1-.1333-.0713c-.0283-.042-.0351-.0986-.021-.1553.0278-.084.1123-.1484.2036-.1562l8.7359-.1123c1.0351-.0489 2.1601-.8868 2.5537-1.9136l.499-1.3013c.0215-.0561.0293-.1128.0147-.168-.5625-2.5463-2.835-4.4453-5.5499-4.4453-2.5039 0-4.6284 1.6177-5.3876 3.8614-.4927-.3658-1.1187-.5625-1.794-.499-1.2026.119-2.1665 1.083-2.2861 2.2856-.0283.31-.0069.6128.0635.894C1.5683 13.171 0 14.7754 0 16.752c0 .1748.0142.3515.0352.5273.0141.083.0844.1475.1689.1475h15.9814c.0909 0 .1758-.0645.2032-.1553l.12-.4268zm2.7568-5.5634c-.0771 0-.1611 0-.2383.0112-.0566 0-.1054.0415-.127.0976l-.3378 1.1744c-.1475.5068-.0918.9707.1543 1.3164.2256.3164.6055.498 1.0625.5195l1.8437.1133c.0557 0 .1055.0263.1329.0703.0283.043.0351.1074.0214.1562-.0283.084-.1132.1485-.204.1553l-1.921.1123c-1.041.0488-2.1582.8867-2.5527 1.914l-.1406.3585c-.0283.0713.0215.1416.0986.1416h6.5977c.0771 0 .1474-.0489.169-.126.1122-.4082.1757-.837.1757-1.2803 0-2.6025-2.125-4.727-4.7344-4.727"

// Brand colours.
var (
	orange = [3]float64{246 / 255.0, 130 / 255.0, 31 / 255.0}
	white  = [3]float64{1, 1, 1}
)

var sizes = []int{16, 24, 32, 48, 64, 72, 256}

func main() {
	if len(os.Args) < 2 {
		panic("usage: gen-icons <icons-dir> [package-dir]")
	}
	iconsDir := os.Args[1]
	if err := os.MkdirAll(iconsDir, 0o755); err != nil {
		panic(err)
	}
	for _, s := range sizes {
		writePNG(filepath.Join(iconsDir, "icon_"+strconv.Itoa(s)+".png"), render(s))
	}

	// Package Center icons live at the SPK root, not inside package.tgz.
	if len(os.Args) >= 3 {
		pkgDir := os.Args[2]
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			panic(err)
		}
		writePNG(filepath.Join(pkgDir, "PACKAGE_ICON.PNG"), render(72))
		writePNG(filepath.Join(pkgDir, "PACKAGE_ICON_256.PNG"), render(256))
	}
}

func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
	_ = f.Close()
}

// point is a position in the 24x24 logo coordinate space.
type point struct{ x, y float64 }

func isSep(c byte) bool {
	return c == ' ' || c == ',' || c == '\n' || c == '\t' || c == '\r'
}

func isLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// parsePath flattens an SVG path made of M/m, L/l, C/c, A/a and Z/z commands into
// polygons. Arcs are approximated by a line to their endpoint; the only arcs in
// the mark are sub-pixel corner roundings.
func parsePath(d string) [][]point {
	var polys [][]point
	var poly []point
	var cur, start point
	var last byte
	i, n := 0, len(d)

	readNum := func() float64 {
		for i < n && isSep(d[i]) {
			i++
		}
		j := i
		if j < n && (d[j] == '+' || d[j] == '-') {
			j++
		}
		for j < n && d[j] >= '0' && d[j] <= '9' {
			j++
		}
		if j < n && d[j] == '.' {
			j++
			for j < n && d[j] >= '0' && d[j] <= '9' {
				j++
			}
		}
		f, _ := strconv.ParseFloat(d[i:j], 64)
		i = j
		return f
	}
	flush := func() {
		if len(poly) >= 3 {
			polys = append(polys, poly)
		}
		poly = nil
	}

	for i < n {
		c := d[i]
		if isSep(c) {
			i++
			continue
		}
		if isLetter(c) {
			if c == 'z' || c == 'Z' {
				poly = append(poly, start)
				flush()
				cur = start
				last = 0
				i++
				continue
			}
			last = c
			i++
		} else if last == 0 {
			panic("number without a command in path")
		}

		switch last {
		case 'M':
			flush()
			cur = point{readNum(), readNum()}
			start = cur
			poly = append(poly, cur)
			last = 'L'
		case 'm':
			flush()
			cur = point{cur.x + readNum(), cur.y + readNum()}
			start = cur
			poly = append(poly, cur)
			last = 'l'
		case 'L':
			cur = point{readNum(), readNum()}
			poly = append(poly, cur)
		case 'l':
			cur = point{cur.x + readNum(), cur.y + readNum()}
			poly = append(poly, cur)
		case 'H':
			cur = point{readNum(), cur.y}
			poly = append(poly, cur)
		case 'h':
			cur = point{cur.x + readNum(), cur.y}
			poly = append(poly, cur)
		case 'V':
			cur = point{cur.x, readNum()}
			poly = append(poly, cur)
		case 'v':
			cur = point{cur.x, cur.y + readNum()}
			poly = append(poly, cur)
		case 'C':
			c1 := point{readNum(), readNum()}
			c2 := point{readNum(), readNum()}
			p3 := point{readNum(), readNum()}
			poly = appendCubic(poly, cur, c1, c2, p3)
			cur = p3
		case 'c':
			c1 := point{cur.x + readNum(), cur.y + readNum()}
			c2 := point{cur.x + readNum(), cur.y + readNum()}
			p3 := point{cur.x + readNum(), cur.y + readNum()}
			poly = appendCubic(poly, cur, c1, c2, p3)
			cur = p3
		case 'A', 'a':
			// rx ry x-axis-rotation large-arc-flag sweep-flag x y
			readNum() // rx
			readNum() // ry
			readNum() // rotation
			readNum() // large-arc
			readNum() // sweep
			x, y := readNum(), readNum()
			if last == 'a' {
				cur = point{cur.x + x, cur.y + y}
			} else {
				cur = point{x, y}
			}
			poly = append(poly, cur)
		default:
			panic("unsupported path command: " + string(last))
		}
	}
	flush()
	return polys
}

func appendCubic(dst []point, p0, p1, p2, p3 point) []point {
	const steps = 24
	for k := 1; k <= steps; k++ {
		t := float64(k) / steps
		mt := 1 - t
		x := mt*mt*mt*p0.x + 3*mt*mt*t*p1.x + 3*mt*t*t*p2.x + t*t*t*p3.x
		y := mt*mt*mt*p0.y + 3*mt*mt*t*p1.y + 3*mt*t*t*p2.y + t*t*t*p3.y
		dst = append(dst, point{x, y})
	}
	return dst
}

// bounds returns the bounding box of all polygons.
func bounds(polys [][]point) (float64, float64, float64, float64) {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, poly := range polys {
		for _, p := range poly {
			minX = math.Min(minX, p.x)
			minY = math.Min(minY, p.y)
			maxX = math.Max(maxX, p.x)
			maxY = math.Max(maxY, p.y)
		}
	}
	return minX, minY, maxX, maxY
}

func inside(polys [][]point, x, y float64) bool {
	wn := 0
	for _, poly := range polys {
		for k := range poly {
			a := poly[k]
			b := poly[(k+1)%len(poly)]
			if a.y <= y {
				if b.y > y && isLeft(a, b, x, y) > 0 {
					wn++
				}
			} else if b.y <= y && isLeft(a, b, x, y) < 0 {
				wn--
			}
		}
	}
	return wn != 0
}

func isLeft(a, b point, x, y float64) float64 {
	return (b.x-a.x)*(y-a.y) - (x-a.x)*(b.y-a.y)
}

func inRoundRect(u, v, x0, y0, x1, y1, r float64) bool {
	cx := math.Min(math.Max(u, x0+r), x1-r)
	cy := math.Min(math.Max(v, y0+r), y1-r)
	dx, dy := u-cx, v-cy
	return dx*dx+dy*dy <= r*r
}

var logoPolys = parsePath(cloudflarePath)

// sampleLogo returns the premultiplied colour at tile coordinates (u, v) in
// [0,1]: an orange rounded tile with the Cloudflare cloud rendered in white.
func sampleLogo(u, v float64) (float64, float64, float64, float64) {
	if !inRoundRect(u, v, 0, 0, 1, 1, 0.22) {
		return 0, 0, 0, 0
	}
	// Map the tile onto the logo coordinate space so the cloud is centred and
	// scaled to FIT of the tile width/height.
	minX, minY, maxX, maxY := bounds(logoPolys)
	bw, bh := maxX-minX, maxY-minY
	scale := math.Min(tileFit/bw, tileFit/bh)
	px := (u-0.5)/scale + (minX+maxX)/2
	py := (v-0.5)/scale + (minY+maxY)/2
	if inside(logoPolys, px, py) {
		return white[0], white[1], white[2], 1
	}
	return orange[0], orange[1], orange[2], 1
}

// tileFit is the fraction of the tile the cloud spans (longest side).
const tileFit = 0.82

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
					r, g, b, a := sampleLogo(u, v)
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
