package token

import (
	"strings"
	"sync"
	"time"

	"github.com/pkoukk/tiktoken-go"
)

const (
	EncodingCL100K = "cl100k_base"
)

var (
	estimator *EstimatorImpl
	once      sync.Once
)

func Init() error {
	once.Do(func() {
		estimator = &EstimatorImpl{
			encoding: EncodingCL100K,
		}
	})
	return nil
}

func GetEstimator() *EstimatorImpl {
	if estimator == nil {
		Init()
	}
	return estimator
}

type EstimatorImpl struct {
	encoding string
}

func (e *EstimatorImpl) CountTokens(text string) int {
	if text == "" {
		return 0
	}

	tke, err := tiktoken.GetEncoding(e.encoding)
	if err != nil {
		return len(text) / 4
	}

	return len(tke.Encode(text, nil, nil))
}

type StreamCounter struct {
	estimator    *EstimatorImpl
	inputTokens  int
	outputTokens int
	startTime    time.Time
	latency      time.Duration
	textBuilder  strings.Builder
	mu           sync.Mutex
}

func NewStreamCounter(inputTokens int) *StreamCounter {
	return &StreamCounter{
		estimator:    GetEstimator(),
		inputTokens:  inputTokens,
		outputTokens: 0,
	}
}

func (c *StreamCounter) SetStartTime(start time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startTime = start
}

func (c *StreamCounter) SetLatency() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.startTime.IsZero() {
		c.latency = time.Since(c.startTime)
	}
}

func (c *StreamCounter) AddOutputTokens(text string) {
	if text == "" {
		return
	}

	tokens := c.estimator.CountTokens(text)

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
		tokens := c.estimator.CountTokens(text)
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

func (c *StreamCounter) GetLatency() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latency
}

func CountTokens(text string) int {
	return GetEstimator().CountTokens(text)
}
