package adminui

import (
	"io/fs"
	"testing"
)

func TestFSEmbed(t *testing.T) {
	// Test that FS is accessible
	var _ fs.FS = FS

	// Test that fonts.css exists
	file, err := FS.Open("static/fonts.css")
	if err != nil {
		t.Fatalf("Failed to open static/fonts.css: %v", err)
	}
	file.Close()

	// Test that favicon exists
	file, err = FS.Open("static/favicon.png")
	if err != nil {
		t.Fatalf("Failed to open static/favicon.png: %v", err)
	}
	file.Close()

	// Providers page assets
	for _, path := range []string{"static/providers.css", "static/providers.js", "static/shared.js"} {
		file, err = FS.Open(path)
		if err != nil {
			t.Fatalf("Failed to open %s: %v", path, err)
		}
		file.Close()
	}

	// Test that all font files exist
	fontFiles := []string{
		"dm-sans-latin-ext.woff2",
		"dm-sans-latin.woff2",
		"dm-serif-display-italic-ext.woff2",
		"dm-serif-display-italic.woff2",
		"dm-serif-display-normal-ext.woff2",
		"dm-serif-display-normal.woff2",
		"jetbrains-mono-latin-ext.woff2",
		"jetbrains-mono-latin.woff2",
	}

	for _, fontFile := range fontFiles {
		file, err := FS.Open("static/fonts/" + fontFile)
		if err != nil {
			t.Errorf("Failed to open static/fonts/%s: %v", fontFile, err)
		} else {
			file.Close()
		}
	}
}
