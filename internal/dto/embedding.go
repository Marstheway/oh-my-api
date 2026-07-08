package dto

import "fmt"

type EmbeddingRequest struct {
	Model          string      `json:"model" binding:"required"`
	Input          interface{} `json:"input" binding:"required"`
	EncodingFormat string      `json:"encoding_format,omitempty"`
	Dimensions     *int        `json:"dimensions,omitempty"`
	User           string      `json:"user,omitempty"`
}

type EmbeddingResponseItem struct {
	Object    string     `json:"object"`
	Index     int        `json:"index"`
	Embedding []float64  `json:"embedding"`
}

type EmbeddingResponse struct {
	Object string                  `json:"object"`
	Data   []EmbeddingResponseItem  `json:"data"`
	Model  string                  `json:"model"`
	Usage  Usage                   `json:"usage"`
}

// ParseInput normalizes and validates the input field.
// Returns a slice of strings where each element is a text to be embedded.
// Accepts either a single string or an array of strings.
// Returns error if:
// - input is neither string nor array
// - input array is empty
// - input array contains non-string elements
// - input array contains mixed types
// Empty strings are valid inputs.
func (r *EmbeddingRequest) ParseInput() ([]string, error) {
	switch v := r.Input.(type) {
	case string:
		return []string{v}, nil
	case []interface{}:
		if len(v) == 0 {
			return nil, fmt.Errorf("input array cannot be empty")
		}

		result := make([]string, len(v))
		for i, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("input array element at index %d is not a string", i)
			}
			result[i] = str
		}
		return result, nil
	default:
		return nil, fmt.Errorf("input must be a string or string array, got %T", v)
	}
}

// IsValidEncodingFormat validates the encoding_format field.
// Only "" (empty string) and "float" are allowed.
func (r *EmbeddingRequest) IsValidEncodingFormat() bool {
	if r.EncodingFormat == "" || r.EncodingFormat == "float" {
		return true
	}
	return false
}
