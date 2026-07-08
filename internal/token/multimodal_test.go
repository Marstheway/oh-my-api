package token

import (
	"testing"
)

func TestEstimateImageTokens(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		detail string
		want   int
	}{
		{
			name:   "zero width returns 0",
			width:  0,
			height: 1000,
			detail: "high",
			want:   0,
		},
		{
			name:   "zero height returns 0",
			width:  1000,
			height: 0,
			detail: "high",
			want:   0,
		},
		{
			name:   "negative width returns 0",
			width:  -100,
			height: 1000,
			detail: "high",
			want:   0,
		},
		{
			name:   "negative height returns 0",
			width:  1000,
			height: -100,
			detail: "high",
			want:   0,
		},
		{
			name:   "low detail returns 85",
			width:  1000,
			height: 1000,
			detail: "low",
			want:   85,
		},
		{
			name:   "low detail with any size returns 85",
			width:  4096,
			height: 4096,
			detail: "low",
			want:   85,
		},
		{
			name:   "high detail small square image 512x512",
			width:  512,
			height: 512,
			detail: "high",
			want:   255, // 1 tile * 170 + 85
		},
		{
			name:   "high detail square image 1024x1024",
			width:  1024,
			height: 1024,
			detail: "high",
			want:   765, // scaled to 768x768, 4 tiles * 170 + 85
		},
		{
			name:   "high detail large square image 2048x2048",
			width:  2048,
			height: 2048,
			detail: "high",
			want:   765, // 4 tiles * 170 + 85
		},
		{
			name:   "high detail rectangular image 1024x768",
			width:  1024,
			height: 768,
			detail: "high",
			want:   765, // no scaling needed, 4 tiles * 170 + 85
		},
		{
			name:   "high detail wide image 2048x1024",
			width:  2048,
			height: 1024,
			detail: "high",
			want:   1105, // scaled to 1536x768, 6 tiles * 170 + 85
		},
		{
			name:   "auto detail uses high logic",
			width:  512,
			height: 512,
			detail: "auto",
			want:   255, // same as high
		},
		{
			name:   "high detail image with min side > 768 gets scaled",
			width:  1536,
			height: 1024,
			detail: "high",
			want:   1105, // scaled to 1152x768, then 6 tiles * 170 + 85
		},
		{
			name:   "high detail tall image",
			width:  768,
			height: 1536,
			detail: "high",
			want:   1105, // scaled to 768x1152, then 6 tiles * 170 + 85
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateImageTokens(tt.width, tt.height, tt.detail)
			if got != tt.want {
				t.Errorf("EstimateImageTokens(%d, %d, %q) = %d, want %d",
					tt.width, tt.height, tt.detail, got, tt.want)
			}
		})
	}
}

func TestEstimateAudioTokens(t *testing.T) {
	tests := []struct {
		name            string
		durationSeconds int
		want            int
	}{
		{
			name:            "0 seconds returns 0",
			durationSeconds: 0,
			want:            0,
		},
		{
			name:            "negative duration returns 0",
			durationSeconds: -10,
			want:            0,
		},
		{
			name:            "10 seconds returns 250",
			durationSeconds: 10,
			want:            250,
		},
		{
			name:            "30 seconds returns 750",
			durationSeconds: 30,
			want:            750,
		},
		{
			name:            "60 seconds returns 1500",
			durationSeconds: 60,
			want:            1500,
		},
		{
			name:            "5 seconds returns 0 (less than 10)",
			durationSeconds: 5,
			want:            0,
		},
		{
			name:            "15 seconds returns 250",
			durationSeconds: 15,
			want:            250,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateAudioTokens(tt.durationSeconds)
			if got != tt.want {
				t.Errorf("EstimateAudioTokens(%d) = %d, want %d",
					tt.durationSeconds, got, tt.want)
			}
		})
	}
}

func TestEstimateVideoTokens(t *testing.T) {
	tests := []struct {
		name            string
		width           int
		height          int
		fps             int
		durationSeconds int
		want            int
	}{
		{
			name:            "0 fps returns 0",
			width:           1920,
			height:          1080,
			fps:             0,
			durationSeconds: 10,
			want:            0,
		},
		{
			name:            "negative fps returns 0",
			width:           1920,
			height:          1080,
			fps:             -30,
			durationSeconds: 10,
			want:            0,
		},
		{
			name:            "0 duration returns 0",
			width:           1920,
			height:          1080,
			fps:             30,
			durationSeconds: 0,
			want:            0,
		},
		{
			name:            "negative duration returns 0",
			width:           1920,
			height:          1080,
			fps:             30,
			durationSeconds: -10,
			want:            0,
		},
		{
			name:            "zero width returns 0",
			width:           0,
			height:          1080,
			fps:             30,
			durationSeconds: 1,
			want:            0,
		},
		{
			name:            "zero height returns 0",
			width:           1920,
			height:          0,
			fps:             30,
			durationSeconds: 1,
			want:            0,
		},
		{
			name:            "negative width returns 0",
			width:           -1920,
			height:          1080,
			fps:             30,
			durationSeconds: 1,
			want:            0,
		},
		{
			name:            "negative height returns 0",
			width:           1920,
			height:          -1080,
			fps:             30,
			durationSeconds: 1,
			want:            0,
		},
		{
			name:            "1 second 30fps 1920x1080",
			width:           1920,
			height:          1080,
			fps:             30,
			durationSeconds: 1,
			want:            2550, // 30 frames * 85 tokens per frame
		},
		{
			name:            "10 seconds 30fps 1920x1080",
			width:           1920,
			height:          1080,
			fps:             30,
			durationSeconds: 10,
			want:            25500, // 300 frames * 85 tokens per frame
		},
		{
			name:            "5 seconds 60fps 1280x720",
			width:           1280,
			height:          720,
			fps:             60,
			durationSeconds: 5,
			want:            25500, // 300 frames * 85 tokens per frame
		},
		{
			name:            "1 second 1fps 512x512",
			width:           512,
			height:          512,
			fps:             1,
			durationSeconds: 1,
			want:            85, // 1 frame * 85 tokens per frame
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateVideoTokens(tt.width, tt.height, tt.fps, tt.durationSeconds)
			if got != tt.want {
				t.Errorf("EstimateVideoTokens(%d, %d, %d, %d) = %d, want %d",
					tt.width, tt.height, tt.fps, tt.durationSeconds, got, tt.want)
			}
		})
	}
}
