package errors

type ErrorCode string

const (
	ErrInvalidAPIKey    ErrorCode = "invalid_api_key"
	ErrInvalidRequest   ErrorCode = "invalid_request"
	ErrModelNotFound    ErrorCode = "model_not_found"
	ErrProviderNotFound ErrorCode = "model_not_found"
	ErrUpstreamError    ErrorCode = "upstream_error"
	ErrUpstreamTimeout  ErrorCode = "upstream_timeout"
	ErrRateLimitTimeout ErrorCode = "rate_limit_timeout"
	ErrInternal         ErrorCode = "internal_error"
	ErrConversionError  ErrorCode = "conversion_error"
)

type GatewayError struct {
	Code       ErrorCode
	Message    string
	StatusCode int
}

func New(code ErrorCode, message string, statusCode int) *GatewayError {
	return &GatewayError{
		Code:       code,
		Message:    message,
		StatusCode: statusCode,
	}
}

func (e *GatewayError) Error() string {
	return e.Message
}
