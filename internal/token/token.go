package token

import (
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

const EncodingCL100K = "cl100k_base"

// Tokenizer counts tokens for a given text.
type Tokenizer interface {
	CountTokens(text string) int
}

var (
	defaultTk  Tokenizer
	deepseekTk Tokenizer
	initMu     sync.Mutex
)

// init ensures defaultTk is never nil, preventing nil-pointer panics in
// CountTokens / NewStreamCounter / Pick when Init has not been called.
func init() {
	defaultTk = &tiktokenTokenizer{encoding: EncodingCL100K}
}

// tiktokenTokenizer uses tiktoken cl100k_base encoding.
type tiktokenTokenizer struct {
	encoding string
}

func (t *tiktokenTokenizer) CountTokens(text string) int {
	if text == "" {
		return 0
	}
	tke, err := tiktoken.GetEncoding(t.encoding)
	if err != nil {
		return len(text) / 4
	}
	return len(tke.Encode(text, nil, nil))
}

// Init initializes the DeepSeek tokenizer from embedded data.
func Init() error {
	initMu.Lock()
	defer initMu.Unlock()

	if deepseekTk != nil {
		return nil
	}

	return loadDeepseekTokenizer()
}

// Pick returns the appropriate Tokenizer for the given upstream model name.
func Pick(upstreamModel string) Tokenizer {
	if deepseekTk != nil && strings.Contains(strings.ToLower(upstreamModel), "deepseek") {
		return deepseekTk
	}
	return defaultTk
}

// CountTokensFor counts tokens using the tokenizer appropriate for upstreamModel.
func CountTokensFor(upstreamModel, text string) int {
	return Pick(upstreamModel).CountTokens(text)
}

// CountTokens counts tokens using the default tiktoken tokenizer.
func CountTokens(text string) int {
	return defaultTk.CountTokens(text)
}

// StreamCounter accumulates input/output token counts for a single request.
type StreamCounter struct {
	tk           Tokenizer
	inputTokens  int
	outputTokens int
	textBuilder  strings.Builder
	mu           sync.Mutex
}

// NewStreamCounterFor creates a StreamCounter using the tokenizer for upstreamModel.
func NewStreamCounterFor(upstreamModel string, inputTokens int) *StreamCounter {
	return &StreamCounter{
		tk:          Pick(upstreamModel),
		inputTokens: inputTokens,
	}
}

// NewStreamCounter creates a StreamCounter using the default tiktoken tokenizer.
func NewStreamCounter(inputTokens int) *StreamCounter {
	return &StreamCounter{
		tk:          defaultTk,
		inputTokens: inputTokens,
	}
}

func (c *StreamCounter) AddOutputTokens(text string) {
	if text == "" {
		return
	}
	tokens := c.tk.CountTokens(text)
	c.mu.Lock()
	c.outputTokens += tokens
	c.mu.Unlock()
}

func (c *StreamCounter) AddOutputText(text string) {
	if text == "" {
		return
	}
	c.mu.Lock()
	c.textBuilder.WriteString(text)
	c.mu.Unlock()
}

func (c *StreamCounter) ComputeOutputTokens() {
	c.mu.Lock()
	text := c.textBuilder.String()
	c.mu.Unlock()

	if text != "" {
		tokens := c.tk.CountTokens(text)
		c.mu.Lock()
		c.outputTokens = tokens
		c.mu.Unlock()
	}
}

func (c *StreamCounter) GetInputTokens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputTokens
}

func (c *StreamCounter) GetOutputTokens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outputTokens
}
