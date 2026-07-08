package codec

import (
	"fmt"
	"strings"
)

type Format string

const (
	FormatOpenAIChat        Format = "openai.chat"
	FormatOpenAIResponse    Format = "openai.responses"
	FormatAnthropicMessages Format = "anthropic.messages"
	FormatOllamaChat        Format = "ollama.chat"
)

func NormalizeProviderFormat(protocol string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case string(FormatOpenAIChat):
		return FormatOpenAIChat, nil
	case string(FormatOpenAIResponse):
		return FormatOpenAIResponse, nil
	case string(FormatAnthropicMessages):
		return FormatAnthropicMessages, nil
	case string(FormatOllamaChat):
		return FormatOllamaChat, nil
	default:
		return "", fmt.Errorf("unknown provider protocol: %s", protocol)
	}
}

func NormalizeProtocols(protocols []string) ([]Format, error) {
	formats := make([]Format, 0, len(protocols))
	for _, part := range protocols {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		format, err := NormalizeProviderFormat(part)
		if err != nil {
			return nil, err
		}
		formats = append(formats, format)
	}
	if len(formats) == 0 {
		return nil, fmt.Errorf("no valid protocols provided")
	}
	return formats, nil
}

func ConversionCost(inbound, outbound Format) (int, error) {
	if inbound == outbound {
		return 0, nil
	}

	matrix := map[Format]map[Format]int{
		FormatOpenAIChat: {
			FormatAnthropicMessages: 2,
			FormatOpenAIResponse:    3,
			FormatOllamaChat:        1,
		},
		FormatAnthropicMessages: {
			FormatOpenAIChat:     3,
			FormatOpenAIResponse: 6,
			FormatOllamaChat:     4,
		},
		FormatOpenAIResponse: {
			FormatOpenAIChat:        3,
			FormatAnthropicMessages: 6,
			FormatOllamaChat:        4,
		},
	}

	if row, ok := matrix[inbound]; ok {
		if cost, ok := row[outbound]; ok {
			return cost, nil
		}
	}

	return 0, fmt.Errorf("unsupported conversion cost: %s -> %s", inbound, outbound)
}

func SelectBestFormat(supported []Format, inbound Format) (Format, string, int, error) {
	if len(supported) == 0 {
		return "", "", 0, fmt.Errorf("no supported outbound format")
	}

	for _, format := range supported {
		if format == inbound {
			return inbound, "passthrough", 0, nil
		}
	}

	best := supported[0]
	bestCost, err := ConversionCost(inbound, best)
	if err != nil {
		return "", "", 0, err
	}

	for _, format := range supported[1:] {
		cost, costErr := ConversionCost(inbound, format)
		if costErr != nil {
			return "", "", 0, costErr
		}
		if cost < bestCost {
			best = format
			bestCost = cost
		}
	}

	return best, "lowest_cost", bestCost, nil
}
