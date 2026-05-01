package codec

import "fmt"

// ConversionError represents a typed codec conversion failure with structured context.
type ConversionError struct {
	Phase          string // e.g. "encode_request", "write_response"
	Step           string // e.g. "response_to_chat", "chat_to_response"
	InboundFormat  string // source format
	OutboundFormat string // target format
	Reason         string // e.g. "unsupported_output_item"
	Err            error  // underlying error
}

func (e *ConversionError) Error() string {
	return fmt.Sprintf("conversion error [%s/%s] %s→%s: %s: %v",
		e.Phase, e.Step, e.InboundFormat, e.OutboundFormat, e.Reason, e.Err)
}

func (e *ConversionError) Unwrap() error {
	return e.Err
}

// WrapConversionError wraps an error as a ConversionError with structured context.
func WrapConversionError(phase, step string, inbound, outbound Format, reason string, err error) error {
	return &ConversionError{
		Phase:          phase,
		Step:           step,
		InboundFormat:  string(inbound),
		OutboundFormat: string(outbound),
		Reason:         reason,
		Err:            err,
	}
}
