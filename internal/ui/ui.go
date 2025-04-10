package ui

import (
	"bytes"
	_ "embed"
	"fmt"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

//go:embed assets/SFMono-Bold.ttf
var fontBytes []byte

func GenerateIconWithText(text string) []byte {
	imgSize := 64
	img := image.NewRGBA(image.Rect(0, 0, imgSize, imgSize))

	// Custom background color
	customColor := color.RGBA{R: 0x0F, G: 0x14, B: 0x17, A: 0xFF}
	draw.Draw(img, img.Bounds(), &image.Uniform{customColor}, image.Point{}, draw.Src)

	// Font loading and text drawing
	ft, err := opentype.Parse(fontBytes)
	if err != nil {
		fmt.Println("Error loading font:", err)
		return nil
	}

	face, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: 38, DPI: 72})
	if err != nil {
		fmt.Println("Error creating font face:", err)
		return nil
	}

	col := color.White
	point := fixed.Point26_6{X: fixed.I(-4), Y: fixed.I(45)}

	d := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{C: col},
		Face: face,
		Dot:  point,
	}
	d.DrawString(text)

	buf := new(bytes.Buffer)
	err = png.Encode(buf, img)
	if err != nil {
		fmt.Println("Error encoding image:", err)
		return nil
	}
	return buf.Bytes()
}
