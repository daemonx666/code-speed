//go:build ignore

// gen draws code-speed's icons: a speedometer gauge.
//
//	go run build/icon/gen.go tray <size> <out.png>   black glyph, macOS template
//	go run build/icon/gen.go dot  <size> <out.png>   coloured disc, Windows and Linux tray
//	go run build/icon/gen.go app  <size> <out.png>   the app icon
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"strconv"
)

const ss = 4 // supersampling per axis

type canvas struct {
	n   int
	acc []color.NRGBA
}

// shape is a signed test: true where (x, y), in 0…1 units, is inside.
type shape func(x, y float64) bool

func (c *canvas) fill(s shape, col color.NRGBA) {
	for py := 0; py < c.n; py++ {
		for px := 0; px < c.n; px++ {
			hit := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(c.n)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(c.n)
					if s(x, y) {
						hit++
					}
				}
			}
			if hit == 0 {
				continue
			}
			a := float64(col.A) / 255 * float64(hit) / (ss * ss)
			d := &c.acc[py*c.n+px]
			da := float64(d.A) / 255
			oa := a + da*(1-a)
			blend := func(s, d uint8) uint8 {
				if oa == 0 {
					return 0
				}
				return uint8((float64(s)*a + float64(d)*da*(1-a)) / oa)
			}
			*d = color.NRGBA{blend(col.R, d.R), blend(col.G, d.G), blend(col.B, d.B), uint8(oa * 255)}
		}
	}
}

func (c *canvas) png(path string) {
	img := image.NewNRGBA(image.Rect(0, 0, c.n, c.n))
	for i, p := range c.acc {
		img.Set(i%c.n, i/c.n, p)
	}
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

// the gauge: a 270° ring open at the bottom, a needle at 70 % and a hub
func arc(cx, cy, r, w float64) shape {
	return func(x, y float64) bool {
		dx, dy := x-cx, y-cy
		d := math.Hypot(dx, dy)
		if d < r-w/2 || d > r+w/2 {
			return false
		}
		a := math.Atan2(dy, dx) * 180 / math.Pi // 0 = right, 90 = down
		return !(a > 45 && a < 135)             // open at the bottom
	}
}

func needle(cx, cy, length, w, frac float64) shape {
	ang := (135 + 270*frac) * math.Pi / 180
	ex, ey := cx+length*math.Cos(ang), cy+length*math.Sin(ang)
	return func(x, y float64) bool {
		vx, vy := ex-cx, ey-cy
		t := ((x-cx)*vx + (y-cy)*vy) / (vx*vx + vy*vy)
		t = math.Max(0, math.Min(1, t))
		return math.Hypot(x-(cx+t*vx), y-(cy+t*vy)) <= w/2
	}
}

func disc(cx, cy, r float64) shape {
	return func(x, y float64) bool { return math.Hypot(x-cx, y-cy) <= r }
}

func roundRect(m, r float64) shape {
	return func(x, y float64) bool {
		qx := math.Max(math.Abs(x-0.5)-(0.5-m-r), 0)
		qy := math.Max(math.Abs(y-0.5)-(0.5-m-r), 0)
		return math.Hypot(qx, qy) <= r
	}
}

func gauge(c *canvas, cx, cy, r, w float64, col color.NRGBA) {
	c.fill(arc(cx, cy, r, w), col)
	c.fill(needle(cx, cy, r*0.78, w*0.9, 0.7), col)
	c.fill(disc(cx, cy, w*0.95), col)
}

var (
	black  = color.NRGBA{0, 0, 0, 255}
	white  = color.NRGBA{255, 255, 255, 255}
	accent = color.NRGBA{16, 185, 129, 255} // emerald
	deep   = color.NRGBA{6, 95, 70, 255}
)

func main() {
	if len(os.Args) != 4 {
		panic("usage: gen tray|dot|app <size> <out.png>")
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil {
		panic(err)
	}
	c := &canvas{n: n, acc: make([]color.NRGBA, n*n)}
	switch os.Args[1] {
	case "tray":
		gauge(c, 0.5, 0.54, 0.38, 0.12, black)
	case "dot":
		c.fill(disc(0.5, 0.5, 0.5), accent)
		gauge(c, 0.5, 0.55, 0.3, 0.1, white)
	case "app":
		c.fill(roundRect(0.06, 0.2), deep)
		c.fill(func(x, y float64) bool { return roundRect(0.06, 0.2)(x, y) && y < 0.62 }, accent)
		gauge(c, 0.5, 0.55, 0.28, 0.07, white)
	default:
		panic("unknown icon " + os.Args[1])
	}
	c.png(os.Args[3])
}
