package codec

import (
	"fmt"
	"strings"
)

type Format string

const (
	FormatOpenAIChat        Format = "openai.chat"
	FormatOpenAIResponse    Format = "openai.response"
	FormatAnthropicMessages Format = "anthropic.messages"
)

func NormalizeProviderFormat(protocol string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai", string(FormatOpenAIChat):
		return FormatOpenAIChat, nil
	case string(FormatOpenAIResponse):
		return FormatOpenAIResponse, nil
	case "anthropic", string(FormatAnthropicMessages):
		return FormatAnthropicMessages, nil
	default:
		return "", fmt.Errorf("unknown provider protocol: %s", protocol)
	}
}

// SelectFormatForInbound chooses the outbound format for a provider that may
// advertise multiple protocols (e.g. "openai/anthropic").
func SelectFormatForInbound(providerProtocol string, inbound Format) (Format, error) {
	parts := strings.Split(strings.TrimSpace(providerProtocol), "/")
	if len(parts) == 1 {
		return NormalizeProviderFormat(parts[0])
	}

	formats := make([]Format, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		format, err := NormalizeProviderFormat(part)
		if err != nil {
			return "", err
		}
		formats = append(formats, format)
	}
	for _, format := range formats {
		if format == inbound {
			return inbound, nil
		}
	}
	if len(formats) > 0 {
		return formats[0], nil
	}
	return "", fmt.Errorf("unknown provider protocol: %s", providerProtocol)
}
