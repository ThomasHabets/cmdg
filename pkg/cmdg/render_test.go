package cmdg

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHTMLRenderFallback(t *testing.T) {
	// Set binary to something that definitely doesn't exist.
	oldBinary := cmdgRenderBinary
	cmdgRenderBinary = "non-existent-binary-12345"
	defer func() { cmdgRenderBinary = oldBinary }()

	input := "<html><body><h1>Hello</h1></body></html>"
	idx := 0
	got, images, err := htmlRender(context.Background(), input, &idx)
	if err != nil {
		t.Fatalf("htmlRender failed: %v", err)
	}

	// Fallback should at least contain the original text or a stripped version.
	if got == "" {
		t.Error("Expected non-empty output in fallback mode")
	}
	if len(images) != 0 {
		t.Errorf("Expected 0 images in fallback mode, got %d", len(images))
	}
}

func TestHTMLRenderExternal(t *testing.T) {
	// Try to find the binary in PATH or common locations.
	bin, err := exec.LookPath(cmdgRenderBinary)
	if err != nil {
		// Try local relative path for development.
		bin = "/home/kira/software/cmdg-image-render/cmdg-image-render"
		if _, err := os.Stat(bin); err != nil {
			t.Skipf("External renderer %q not found, skipping integration test", cmdgRenderBinary)
			return
		}
	}

	// Set binary to the one we found.
	oldBinary := cmdgRenderBinary
	cmdgRenderBinary = bin
	defer func() { cmdgRenderBinary = oldBinary }()

	input := "<html><body><h1>Hello</h1><img src=\"cid:img1\"></body></html>"
	idx := 10
	got, images, err := htmlRender(context.Background(), input, &idx)
	if err != nil {
		t.Fatalf("htmlRender failed: %v", err)
	}

	// Should contain the marker.
	if !strings.Contains(got, " ##IMG_10_## ") {
		t.Errorf("Expected output to contain marker ##IMG_10_##, got %q", got)
	}
	if len(images) != 1 {
		t.Errorf("Expected 1 image, got %d", len(images))
	}
	if images[0].Source != "cid:img1" {
		t.Errorf("Expected image source cid:img1, got %q", images[0].Source)
	}
}
