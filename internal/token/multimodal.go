package token

// EstimateImageTokens 基于 OpenAI 官方规则估算图片 Token 数量
// 规则：
//   - detail="low": 固定返回 85 tokens
//   - detail="high" 或 "auto":
//     1. 将图片缩放到 fit within 2048x2048
//     2. 如果最短边 > 768px，缩放到最短边 = 768px
//     3. 计算 512x512 tiles 数量
//     4. 总计 = tiles * 170 + 85
func EstimateImageTokens(width, height int, detail string) int {
	if width <= 0 || height <= 0 {
		return 0
	}

	if detail == "low" {
		return 85
	}

	// detail is "high" or "auto"
	w, h := width, height

	// Step 1: Scale to fit within 2048x2048 while maintaining aspect ratio
	maxDim := 2048
	if w > maxDim || h > maxDim {
		scale := float64(maxDim) / float64(max(w, h))
		w = int(float64(w) * scale)
		h = int(float64(h) * scale)
	}

	// Step 2: If shortest side > 768, scale so shortest side = 768
	minSide := min(w, h)
	if minSide > 768 {
		scale := 768.0 / float64(minSide)
		w = int(float64(w) * scale)
		h = int(float64(h) * scale)
	}

	// Step 3: Calculate tiles (512x512)
	tiles := calculateTiles(w, h)

	// Step 4: tokens = tiles * 170 + 85
	return tiles*170 + 85
}

// calculateTiles 计算 512x512 tiles 数量
// 向上取整，例如 513x512 需要 2x1=2 tiles
func calculateTiles(w, h int) int {
	tileSize := 512
	tilesX := (w + tileSize - 1) / tileSize
	tilesY := (h + tileSize - 1) / tileSize
	return tilesX * tilesY
}

// EstimateAudioTokens 基于音频时长估算 Token 数量
// 规则：每 10 秒约 250 tokens
func EstimateAudioTokens(durationSeconds int) int {
	if durationSeconds <= 0 {
		return 0
	}
	return (durationSeconds / 10) * 250
}

// EstimateVideoTokens 基于视频参数估算 Token 数量
// 规则：基于图片帧，每帧按图片规则计算（使用 low detail）
// tokens = 帧数 * 每帧图片tokens = fps * durationSeconds * EstimateImageTokens(width, height, "low")
func EstimateVideoTokens(width, height, fps, durationSeconds int) int {
	if durationSeconds <= 0 || fps <= 0 || width <= 0 || height <= 0 {
		return 0
	}

	// 视频总帧数
	frameCount := fps * durationSeconds

	// 每帧按图片 low detail 计算
	tokensPerFrame := EstimateImageTokens(width, height, "low")

	return frameCount * tokensPerFrame
}
