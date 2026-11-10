package ipad

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestRenderShot(t *testing.T) {
	w, c := newFake(t)
	// A small screen keeps the race-detector run fast: 200x100 points at 2x,
	// the red quarter is x < 100, y < 50 points.
	w.Window = [2]float64{200, 100}
	raw, err := c.Screenshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Whole screen at one pixel per point: 200x100 JPEG.
	shot, err := RenderShot(raw, 2, ShotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if shot.MIMEType != "image/jpeg" || shot.Width != 200 || shot.Height != 100 || shot.PPP != 1 {
		t.Fatalf("default shot = %+v", shot)
	}
	if _, err := jpeg.Decode(bytes.NewReader(shot.Data)); err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}

	// A crop straddling the red quarter's edge, at full Retina detail, as PNG.
	shot, err = RenderShot(raw, 2, ShotOptions{Region: &Rect{X: 95, Y: 0, Width: 20, Height: 10}, PixelsPerPoint: 3, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	if shot.Width != 40 || shot.Height != 20 || shot.PPP != 2 {
		t.Fatalf("crop = %+v (ppp is capped at the device scale)", shot)
	}
	img, err := png.Decode(bytes.NewReader(shot.Data))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _, _ := img.At(2, 2).RGBA(); r>>8 != 200 {
		t.Fatalf("left of x=100 is red, got r=%d", r>>8)
	}
	if r, _, _, _ := img.At(38, 2).RGBA(); r>>8 != 30 {
		t.Fatalf("right of x=100 is grey, got r=%d", r>>8)
	}

	// Shrinking averages: a half-point-per-pixel shot of the edge blends.
	shot, err = RenderShot(raw, 2, ShotOptions{Region: &Rect{X: 99, Y: 0, Width: 2, Height: 2}, PixelsPerPoint: 0.5, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	img, _ = png.Decode(bytes.NewReader(shot.Data))
	if img.Bounds() != image.Rect(0, 0, 1, 1) {
		t.Fatalf("bounds = %v", img.Bounds())
	}
	if r, _, _, _ := img.At(0, 0).RGBA(); r>>8 != 115 {
		t.Fatalf("averaged red = %d, want 115", r>>8)
	}

	// A region clipped to the screen; one fully outside is an error.
	shot, err = RenderShot(raw, 2, ShotOptions{Region: &Rect{X: 190, Y: 86, Width: 100, Height: 100}})
	if err != nil || shot.Region.Width != 10 || shot.Region.Height != 14 {
		t.Fatalf("clipped region = %+v, %v", shot.Region, err)
	}
	if _, err := RenderShot(raw, 2, ShotOptions{Region: &Rect{X: 2000, Y: 0, Width: 10, Height: 10}}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside region error = %v", err)
	}
	if _, err := RenderShot(raw, 2, ShotOptions{Format: "gif"}); err == nil {
		t.Fatal("gif accepted")
	}
}
