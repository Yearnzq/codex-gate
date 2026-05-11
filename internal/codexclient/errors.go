package codexclient

import (
	"errors"
	"fmt"

	"codex-gate/internal/redaction"
)

type ErrorKind string

const (
	KindUnauthorized       ErrorKind = "unauthorized"
	KindForbidden          ErrorKind = "forbidden"
	KindTimeout            ErrorKind = "timeout"
	KindRateLimited        ErrorKind = "rate_limited"
	KindServerError        ErrorKind = "server_error"
	KindStreamFailed       ErrorKind = "stream_failed"
	KindNetworkTimeout     ErrorKind = "network_timeout"
	KindMalformedResponse  ErrorKind = "malformed_response"
	KindTransportFailed    ErrorKind = "transport_failed"
	KindRequestBuildFailed ErrorKind = "request_build_failed"
)

type ClientError struct {
	Kind       ErrorKind
	StatusCode int
	RequestID  string
	Retryable  bool
	Message    string
	Cause      error
}

func (e *ClientError) Error() string {
	base := fmt.Sprintf("codex client error kind=%s", e.Kind)
	if e.StatusCode > 0 {
		base += fmt.Sprintf(" status=%d", e.StatusCode)
	}
	if e.RequestID != "" {
		base += fmt.Sprintf(" request_id=%s", redaction.RedactText(e.RequestID))
	}
	if e.Message != "" {
		base += ": " + redaction.RedactText(e.Message)
	}
	return base
}

func (e *ClientError) Unwrap() error {
	return e.Cause
}

func classifyHTTPError(statusCode int, requestID string, responseBody []byte) *ClientError {
	message := extractErrorMessage(responseBody)
	switch statusCode {
	case 401:
		return &ClientError{
			Kind:       KindUnauthorized,
			StatusCode: statusCode,
			RequestID:  requestID,
			Retryable:  false,
			Message:    firstNonEmpty(message, "authentication required"),
		}
	case 403:
		return &ClientError{
			Kind:       KindForbidden,
			StatusCode: statusCode,
			RequestID:  requestID,
			Retryable:  false,
			Message:    firstNonEmpty(message, "permission denied"),
		}
	case 408, 504:
		return &ClientError{
			Kind:       KindTimeout,
			StatusCode: statusCode,
			RequestID:  requestID,
			Retryable:  true,
			Message:    firstNonEmpty(message, "upstream timeout"),
		}
	case 429:
		return &ClientError{
			Kind:       KindRateLimited,
			StatusCode: statusCode,
			RequestID:  requestID,
			Retryable:  true,
			Message:    firstNonEmpty(message, "rate limited"),
		}
	default:
		if statusCode >= 500 {
			return &ClientError{
				Kind:       KindServerError,
				StatusCode: statusCode,
				RequestID:  requestID,
				Retryable:  true,
				Message:    firstNonEmpty(message, "server error"),
			}
		}
		return &ClientError{
			Kind:       KindMalformedResponse,
			StatusCode: statusCode,
			RequestID:  requestID,
			Retryable:  false,
			Message:    firstNonEmpty(message, "unexpected non-success HTTP status"),
		}
	}
}

func toClientError(err error) *ClientError {
	if err == nil {
		return &ClientError{
			Kind:      KindMalformedResponse,
			Retryable: false,
			Message:   "unknown error",
		}
	}
	var typed *ClientError
	if errors.As(err, &typed) {
		return typed
	}
	return &ClientError{
		Kind:      KindTransportFailed,
		Retryable: false,
		Message:   err.Error(),
		Cause:     err,
	}
}

func firstNonEmpty(values ...string) string {
	for _, item := range values {
		if item != "" {
			return item
		}
	}
	return ""
}
