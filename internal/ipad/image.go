package ipad

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
)

// Screen is the device screen in points plus the pixels-per-point factor of
// its screenshots (2 on Retina iPads).
type Screen struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Scale  float64 `json:"scale"`
}

// ScreenInfo reads WDA's sessionless /wda/screen.
func (c *Client) ScreenInfo(ctx context.Context) (Screen, error) {
	var v struct {
		Scale      float64 `json:"scale"`
		ScreenSize Size    `json:"screenSize"`
	}
	if err := c.call(ctx, http.MethodGet, "/wda/screen", nil, &v); err != nil {
		return Screen{}, err
	}
	return Screen{Width: v.ScreenSize.Width, Height: v.ScreenSize.Height, Scale: v.Scale}, nil
}

// ShotOptions shapes a screenshot for an agent.
type ShotOptions struct {
	// Region crops to this rect, in points (nil = the whole screen).
	Region *Rect
	// PixelsPerPoint is the output resolution: 1 (the default) makes one image
	// pixel one point, so pixel (px,py) of the image is point
	// (Region.X+px, Region.Y+py) — directly usable with ipad_tap. Use the
	// device scale (2) for full detail on a small crop.
	PixelsPerPoint float64
	// Format is "jpeg" (default) or "png".
	Format string
	// Quality is the JPEG quality (default 85).
	Quality int
}

// Shot is an encoded, cropped and scaled screenshot.
type Shot struct {
	Data     []byte  `json:"-"`
	MIMEType string  `json:"mime_type"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Region   Rect    `json:"region"`
	PPP      float64 `json:"pixels_per_point"`
}

// RenderShot decodes a device PNG and applies o. pngScale is the source's
// pixels per point (Screen.Scale; ≤0 = 1).
func RenderShot(pngData []byte, pngScale float64, o ShotOptions) (Shot, error) {
	src, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return Shot{}, fmt.Errorf("decode screenshot: %w", err)
	}
	if pngScale <= 0 {
		pngScale = 1
	}
	ppp := o.PixelsPerPoint
	if ppp <= 0 {
		ppp = 1
	}
	if ppp > pngScale {
		ppp = pngScale
	}
	b := src.Bounds()
	full := Rect{Width: float64(b.Dx()) / pngScale, Height: float64(b.Dy()) / pngScale}
	region := full
	if o.Region != nil {
		region = intersect(*o.Region, full)
		if region.Empty() {
			return Shot{}, fmt.Errorf("region %s lies outside the %gx%g-point screen", o.Region, full.Width, full.Height)
		}
	}
	px := image.Rect(
		b.Min.X+int(math.Floor(region.X*pngScale)), b.Min.Y+int(math.Floor(region.Y*pngScale)),
		b.Min.X+int(math.Ceil((region.X+region.Width)*pngScale)), b.Min.Y+int(math.Ceil((region.Y+region.Height)*pngScale)),
	).Intersect(b)
	w := max(1, int(math.Round(region.Width*ppp)))
	h := max(1, int(math.Round(region.Height*ppp)))
	out := resample(src, px, w, h)

	var buf bytes.Buffer
	mime := "image/jpeg"
	switch o.Format {
	case "png":
		mime = "image/png"
		err = png.Encode(&buf, out)
	case "", "jpeg", "jpg":
		q := o.Quality
		if q <= 0 || q > 100 {
			q = 85
		}
		err = jpeg.Encode(&buf, out, &jpeg.Options{Quality: q})
	default:
		return Shot{}, fmt.Errorf("unknown image format %q (jpeg or png)", o.Format)
	}
	if err != nil {
		return Shot{}, fmt.Errorf("encode screenshot: %w", err)
	}
	return Shot{Data: buf.Bytes(), MIMEType: mime, Width: w, Height: h, Region: region, PPP: ppp}, nil
}

func intersect(a, b Rect) Rect {
	x0, y0 := math.Max(a.X, b.X), math.Max(a.Y, b.Y)
	x1, y1 := math.Min(a.X+a.Width, b.X+b.Width), math.Min(a.Y+a.Height, b.Y+b.Height)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}
}

// resample scales the r part of src to w×h: an area average when shrinking
// (text stays legible), nearest neighbour otherwise.
func resample(src image.Image, r image.Rectangle, w, h int) *image.RGBA {
	in := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(in, in.Bounds(), src, r.Min, draw.Src)
	if r.Dx() == w && r.Dy() == h {
		return in
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	fx := float64(r.Dx()) / float64(w)
	fy := float64(r.Dy()) / float64(h)
	for y := 0; y < h; y++ {
		sy0 := int(float64(y) * fy)
		sy1 := max(sy0+1, min(r.Dy(), int(math.Ceil(float64(y+1)*fy))))
		for x := 0; x < w; x++ {
			sx0 := int(float64(x) * fx)
			sx1 := max(sx0+1, min(r.Dx(), int(math.Ceil(float64(x+1)*fx))))
			var sr, sg, sb, sa, n uint32
			for sy := sy0; sy < sy1; sy++ {
				off := sy*in.Stride + sx0*4
				for sx := sx0; sx < sx1; sx++ {
					sr += uint32(in.Pix[off])
					sg += uint32(in.Pix[off+1])
					sb += uint32(in.Pix[off+2])
					sa += uint32(in.Pix[off+3])
					off += 4
					n++
				}
			}
			o := y*out.Stride + x*4
			out.Pix[o] = uint8(sr / n)
			out.Pix[o+1] = uint8(sg / n)
			out.Pix[o+2] = uint8(sb / n)
			out.Pix[o+3] = uint8(sa / n)
		}
	}
	return out
}
