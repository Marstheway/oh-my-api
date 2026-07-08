package dto

// 多模态内容类型常量
// 用于 Codec 和其他模块识别不同类型的内容块
const (
	// OpenAI 协议内容类型
	ContentTypeText       = "text"
	ContentTypeImageURL   = "image_url"
	ContentTypeInputAudio = "input_audio"
	ContentTypeFile       = "file"
	ContentTypeVideoURL   = "video_url"

	// Anthropic 协议内容类型
	ContentTypeImage    = "image"
	ContentTypeDocument = "document"
)
