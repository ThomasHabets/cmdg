package cmdg

import (
	"bytes"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"testing"
	"strings"
)

func TestImageDimensionDecoding(t *testing.T) {
	// 1x1 white PNG
	pngData := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
		0x00, 0x05, 0xfe, 0x02, 0xfe, 0xdc, 0x44, 0x74, 0x8e, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(pngData))
	if err != nil {
		t.Fatalf("Failed to decode PNG config: %v", err)
	}
	if cfg.Width != 1 || cfg.Height != 1 {
		t.Errorf("Expected 1x1, got %dx%d", cfg.Width, cfg.Height)
	}
}

func TestCalculateCellOccupancy(t *testing.T) {
	// Assume 10x20 pixels per cell (0.5 ratio)
	for _, test := range []struct {
		pixelW, pixelH int
		termW, termH   int
		wantW, wantH   int
	}{
		{100, 100, 80, 24, 10, 5},
		{500, 100, 80, 24, 50, 5},
		{1000, 1000, 20, 10, 20, 10}, // Scale to fit width
	} {
		gotW, gotH := calculateCellOccupancy(test.pixelW, test.pixelH, test.termW, test.termH)
		if gotW != test.wantW || gotH != test.wantH {
			t.Errorf("For %dx%d in %dx%d got %dx%d, want %dx%d", test.pixelW, test.pixelH, test.termW, test.termH, gotW, gotH, test.wantW, test.wantH)
		}
	}
}

func TestKittyEncoder(t *testing.T) {
	data := []byte("fake image data")
	got := KittyEncode(data, 10, 5)
	if !strings.HasPrefix(got, "\x1b_G") || !strings.HasSuffix(got, "\x1b\\") {
		t.Errorf("Invalid Kitty sequence: %q", got)
	}
	if !strings.Contains(got, "a=T,t=d,c=10,r=5") {
		t.Errorf("Kitty sequence missing parameters: %q", got)
	}
}

func TestITerm2Encoder(t *testing.T) {
	msg := &Message{}
	data := []byte("fake image data")
	got := msg.ITerm2Encode(data, 10, 5)
	if !strings.HasPrefix(got, "\x1b]1337;File=") || (!strings.HasSuffix(got, "\x07") && !strings.HasSuffix(got, "\x1b\\")) {
		t.Errorf("Invalid iTerm2 sequence: %q", got)
	}
	if !strings.Contains(got, "width=10ch;height=5lp;inline=1") {
		t.Errorf("iTerm2 sequence missing parameters: %q", got)
	}
}

func TestImageInViewport(t *testing.T) {
	img := &InlineImage{Y: 10, Height: 5}
	headerLines := 8
	screenHeight := 24

	tests := []struct {
		scroll  int
		visible bool
	}{
		{0, true},  // screenY=18, end=23. Inclusive logic returns true.
		{1, true},  // screenY=17, end=22.
		{5, true},  // screenY=13, end=18.
		{10, true}, // screenY=8, end=13.
		{11, true}, // screenY=7, end=12. Partially visible.
	}

	for _, test := range tests {
		got := img.InViewport(test.scroll, headerLines, screenHeight)
		if got != test.visible {
			t.Errorf("For scroll %d expected visible=%v, got %v", test.scroll, test.visible, got)
		}
	}
}

