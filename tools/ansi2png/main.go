package main

import (
	"bufio"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func main() {
	data, _ := os.ReadFile(os.Args[1])
	fontData, _ := os.ReadFile(os.Args[3])
	parsed, err := opentype.Parse(fontData)
	if err != nil {
		panic(err)
	}
	face, _ := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 13, DPI: 144, Hinting: font.HintingFull})
	boldData, _ := os.ReadFile(strings.Replace(os.Args[3], "Regular", "Bold", 1))
	boldParsed, _ := opentype.Parse(boldData)
	bold, _ := opentype.NewFace(boldParsed, &opentype.FaceOptions{Size: 13, DPI: 144, Hinting: font.HintingFull})
	adv, _ := face.GlyphAdvance('M')
	cw := adv.Ceil()
	ch := 2 * cw
	lines := strings.Split(string(data), "\n")
	cols := 0
	for _, l := range lines {
		_ = l
	}
	cols = 120
	if len(os.Args) > 4 {
		cols, _ = strconv.Atoi(os.Args[4])
	}
	bg0 := color.RGBA{9, 9, 11, 255}
	img := image.NewRGBA(image.Rect(0, 0, cols*cw, len(lines)*ch))
	draw.Draw(img, img.Bounds(), &image.Uniform{bg0}, image.Point{}, draw.Src)
	fg0 := color.RGBA{250, 250, 250, 255}
	for row, line := range lines {
		fg, bg := fg0, bg0
		isBold, faint := false, false
		col := 0
		r := bufio.NewReader(strings.NewReader(line))
		for {
			c, _, err := r.ReadRune()
			if err != nil {
				break
			}
			if c == 0x1b {
				r.ReadRune() // [
				seq := ""
				for {
					d, _, _ := r.ReadRune()
					if d == 'm' {
						break
					}
					seq += string(d)
				}
				parts := strings.Split(seq, ";")
				for i := 0; i < len(parts); i++ {
					switch parts[i] {
					case "", "0":
						fg, bg, isBold, faint = fg0, bg0, false, false
					case "1":
						isBold = true
					case "2":
						faint = true
					case "22":
						isBold, faint = false, false
					case "39":
						fg = fg0
					case "49":
						bg = bg0
					case "38", "48":
						if i+4 < len(parts) && parts[i+1] == "2" {
							rr, _ := strconv.Atoi(parts[i+2])
							gg, _ := strconv.Atoi(parts[i+3])
							bb, _ := strconv.Atoi(parts[i+4])
							cc := color.RGBA{uint8(rr), uint8(gg), uint8(bb), 255}
							if parts[i] == "38" {
								fg = cc
							} else {
								bg = cc
							}
							i += 4
						}
					}
				}
				continue
			}
			x, y := col*cw, row*ch
			draw.Draw(img, image.Rect(x, y, x+cw, y+ch), &image.Uniform{bg}, image.Point{}, draw.Src)
			f := fg
			if faint {
				f = color.RGBA{uint8((int(fg.R) + int(bg.R)) / 2), uint8((int(fg.G) + int(bg.G)) / 2), uint8((int(fg.B) + int(bg.B)) / 2), 255}
			}
			switch c {
			case '▀':
				draw.Draw(img, image.Rect(x, y, x+cw, y+ch/2), &image.Uniform{f}, image.Point{}, draw.Src)
			case '▄':
				draw.Draw(img, image.Rect(x, y+ch/2, x+cw, y+ch), &image.Uniform{f}, image.Point{}, draw.Src)
			case '█':
				draw.Draw(img, image.Rect(x, y, x+cw, y+ch), &image.Uniform{f}, image.Point{}, draw.Src)
			case '▌':
				draw.Draw(img, image.Rect(x, y, x+cw/2, y+ch), &image.Uniform{f}, image.Point{}, draw.Src)
			case ' ':
			default:
				fc := face
				if isBold {
					fc = bold
				}
				d := font.Drawer{Dst: img, Src: &image.Uniform{f}, Face: fc, Dot: fixed.P(x, y+ch*3/4)}
				d.DrawString(string(c))
			}
			col++
		}
	}
	out, _ := os.Create(os.Args[2])
	png.Encode(out, img)
	out.Close()
}
