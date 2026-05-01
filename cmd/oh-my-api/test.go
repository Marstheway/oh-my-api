package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/adaptor"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/provider"
)

func runTest(configPath, internalName string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	idx := strings.Index(internalName, "/")
	if idx <= 0 || idx == len(internalName)-1 {
		fmt.Println("Error: invalid model format, expected 'provider/model'")
		fmt.Println("Example: openai/gpt-4o")
		os.Exit(1)
	}
	providerName := internalName[:idx]
	upstreamModel := internalName[idx+1:]

	providerCfg, ok := cfg.Providers.Items[providerName]
	if !ok {
		fmt.Printf("Error: provider '%s' not found\n", providerName)
		fmt.Println("Available providers:")
		for name := range cfg.Providers.Items {
			fmt.Printf("  %s\n", name)
		}
		os.Exit(1)
	}

	client := provider.NewClient(map[string]config.ProviderConfig{
		providerName: providerCfg,
	}, cfg.Providers.Timeout)

	testReq := &dto.ChatCompletionRequest{
		Model: upstreamModel,
		Messages: []dto.Message{
			{Role: "user", Content: "你好，请简单介绍下自己。"},
		},
		MaxTokens: 1000,
	}

	bodyBytes, err := json.Marshal(testReq)
	if err != nil {
		fmt.Printf("Error: failed to marshal request: %v\n", err)
		os.Exit(1)
	}

	// 收集所有需要测试的 endpoint
	var testEndpoints []struct {
		url      string
		protocol string
	}
	if len(providerCfg.Endpoints) > 0 {
		for _, ep := range providerCfg.Endpoints {
			testEndpoints = append(testEndpoints, struct {
				url      string
				protocol string
			}{url: ep.URL, protocol: ep.Protocol})
		}
	} else if providerCfg.Endpoint != "" {
		testEndpoints = append(testEndpoints, struct {
			url      string
			protocol string
		}{url: providerCfg.Endpoint, protocol: providerCfg.Protocol})
	}

	if len(testEndpoints) == 0 {
		fmt.Println("Error: no endpoint configured for this provider")
		os.Exit(1)
	}

	fmt.Printf("Testing %s...\n", internalName)
	fmt.Printf("Total endpoints: %d\n\n", len(testEndpoints))

	hasFailure := false
	for i, ep := range testEndpoints {
		fmt.Printf("=== Endpoint %d/%d (%s) ===\n", i+1, len(testEndpoints), ep.protocol)
		fmt.Printf("URL: %s\n", ep.url)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		// 直接使用 endpoint 的 URL 和 protocol 构建请求
		requestURL := adaptor.BuildURL(ep.url, adaptor.Protocol(ep.protocol))
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(bodyBytes))
		if err != nil {
			fmt.Printf("Error: failed to create request: %v\n\n", err)
			hasFailure = true
			cancel()
			continue
		}

		httpReq.Header.Set("Content-Type", "application/json")
		switch adaptor.Protocol(ep.protocol) {
		case adaptor.ProtocolAnthropic:
			httpReq.Header.Set("x-api-key", providerCfg.APIKey)
			httpReq.Header.Set("anthropic-version", "2023-06-01")
		default:
			httpReq.Header.Set("Authorization", "Bearer "+providerCfg.APIKey)
		}

		fmt.Printf("Request URL: %s\n", httpReq.URL.String())

		start := time.Now()
		resp, err := client.Do(providerName, httpReq)
		latency := time.Since(start)

		if err != nil {
			fmt.Printf("FAILED: %v\n", err)
			fmt.Printf("Latency: %s\n\n", latency.Round(time.Millisecond))
			hasFailure = true
			cancel()
			continue
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			fmt.Printf("SUCCESS: HTTP %d\n", resp.StatusCode)
			fmt.Printf("Latency: %s\n", latency.Round(time.Millisecond))

			var chatResp dto.ChatCompletionResponse
			if err := json.Unmarshal(body, &chatResp); err != nil {
				fmt.Printf("Parse error: %v\n", err)
				fmt.Printf("Raw response: %s\n", string(body))
			} else {
				if len(chatResp.Choices) > 0 {
					msg := chatResp.Choices[0].Message
					if msg != nil && msg.Content != "" {
						v := msg.Content
						if len(v) > 200 {
							v = v[:200] + "..."
						}
						fmt.Printf("Response: %s\n", v)
					}
					if chatResp.Choices[0].FinishReason != nil {
						fmt.Printf("FinishReason: %s\n", *chatResp.Choices[0].FinishReason)
					}
				}
				if chatResp.Usage.TotalTokens > 0 {
					fmt.Printf("Usage: prompt=%d, completion=%d, total=%d\n", chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens, chatResp.Usage.TotalTokens)
					if chatResp.Usage.CompletionTokens > 0 && latency.Milliseconds() > 0 {
						tps := float64(chatResp.Usage.CompletionTokens) / latency.Seconds()
						fmt.Printf("Throughput: %.1f tokens/s\n", tps)
					}
				}
				if chatResp.Model != "" {
					fmt.Printf("Model: %s\n", chatResp.Model)
				}
			}
		} else {
			fmt.Printf("FAILED: HTTP %d\n", resp.StatusCode)
			fmt.Printf("Latency: %s\n", latency.Round(time.Millisecond))
			fmt.Printf("Response: %s", string(body))
			hasFailure = true
		}
		fmt.Println()

		cancel()
	}

	if hasFailure {
		os.Exit(1)
	}
}
