package elephas

import "fmt"

type ErrorCode string

const (
	ErrorCodeNotFound             ErrorCode = "not_found"
	ErrorCodeInvalidRequest       ErrorCode = "invalid_request"
	ErrorCodeConflict             ErrorCode = "conflict"
	ErrorCodeExtractionFailed     ErrorCode = "extraction_failed"
	ErrorCodeStore                ErrorCode = "store_error"
	ErrorCodeExtractorUnavailable ErrorCode = "extractor_unavailable"
)

type Error struct {
	Code    ErrorCode
	Message string
	Details map[string]any
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}

	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewError(code ErrorCode, message string, details map[string]any) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Details: details,
	}
}

func WrapError(code ErrorCode, message string, err error, details map[string]any) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Details: details,
		Err:     err,
	}
}
