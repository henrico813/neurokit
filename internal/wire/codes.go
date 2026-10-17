// Package wire defines what crosses HTTP between the neurokit client and a
// broker, and what a handler writes on the answer pipe: the routes and spec
// header, the reply body, the shared codes and their HTTP statuses, the write
// outcome, and strict JSON decoding.
package wire

import "net/http"

const (
	CodeInvalidRequest      = "invalid-request"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not-found"
	CodeMethodNotAllowed    = "method-not-allowed"
	CodeRequestTooLarge     = "request-too-large"
	CodeUnexpected          = "unexpected"
	CodeProviderUnavailable = "provider-unavailable"
	CodeProviderRejected    = "provider-rejected"
	CodeChangeUnknown       = "change-unknown"
	CodeVerificationFailed  = "verification-failed"
	CodeSpecMismatch        = "spec-mismatch"
	CodeNotReady            = "not-ready"
	CodeDeadlineExceeded    = "deadline-exceeded"
	CodeBrokerUnavailable   = "broker-unavailable"
)

const DeclaredCodeStatus = http.StatusUnprocessableEntity

var statuses = map[string]int{
	CodeInvalidRequest:      http.StatusBadRequest,
	CodeForbidden:           http.StatusForbidden,
	CodeNotFound:            http.StatusNotFound,
	CodeMethodNotAllowed:    http.StatusMethodNotAllowed,
	CodeRequestTooLarge:     http.StatusRequestEntityTooLarge,
	CodeUnexpected:          http.StatusInternalServerError,
	CodeProviderUnavailable: http.StatusBadGateway,
	CodeProviderRejected:    http.StatusBadGateway,
	CodeChangeUnknown:       http.StatusBadGateway,
	CodeVerificationFailed:  http.StatusBadGateway,
	CodeSpecMismatch:        http.StatusBadRequest,
	CodeNotReady:            http.StatusServiceUnavailable,
	CodeDeadlineExceeded:    http.StatusGatewayTimeout,
	CodeBrokerUnavailable:   0,
}

func IsSharedCode(code string) bool {
	_, ok := statuses[code]
	return ok
}

func LookupHTTPStatus(code string) int {
	if status, ok := statuses[code]; ok {
		return status
	}
	return DeclaredCodeStatus
}

type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}
