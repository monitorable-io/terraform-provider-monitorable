package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is a failed API call: the problem+json document of the response,
// or, when the body is not one, a synthesised equivalent (code "http_<status>").
type APIError struct {
	Status    int
	Code      string
	Title     string
	Detail    string
	RequestID string
}

// Error renders "<title> (<code>): <detail> [request_id <id>]".
func (e *APIError) Error() string {
	s := fmt.Sprintf("%s (%s): %s", e.Title, e.Code, e.Detail)
	if e.RequestID != "" {
		s += " [request_id " + e.RequestID + "]"
	}
	return s
}

// IsNotFound reports whether the API itself answered 404 not_found. A 404
// without a problem body (code "http_404", for example an edge or proxy error
// page) is not the API saying the object is gone, so it does not count.
func (e *APIError) IsNotFound() bool {
	return e.Status == http.StatusNotFound && e.Code == "not_found"
}

// IsNotFound reports whether err is an APIError for which (*APIError).IsNotFound
// is true.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.IsNotFound()
}

// IsPlanLimit reports whether err is the plan_limit_reached refusal (402).
func IsPlanLimit(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == "plan_limit_reached"
}

// problemBody is the RFC 9457 document the API returns on failure.
type problemBody struct {
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
}

// newAPIError builds the error for a non-2xx response. Any occurrence of the
// bearer in the fields is redacted, so a server that echoes credentials back
// cannot leak them into a diagnostic.
func newAPIError(status int, header http.Header, body []byte, secret string) *APIError {
	ae := &APIError{Status: status, RequestID: header.Get("X-Request-Id")}
	var p problemBody
	if looksJSON(header) && json.Unmarshal(body, &p) == nil && p.Code != "" {
		ae.Code, ae.Title, ae.Detail = p.Code, p.Title, p.Detail
		if p.RequestID != "" {
			ae.RequestID = p.RequestID
		}
	}
	text := http.StatusText(status)
	if text == "" {
		text = fmt.Sprintf("HTTP %d", status)
	}
	if ae.Code == "" {
		ae.Code = fmt.Sprintf("http_%d", status)
		ae.Detail = text
	}
	if ae.Title == "" {
		ae.Title = text
	}
	if ae.Detail == "" {
		ae.Detail = ae.Title
	}
	redact := func(s string) string {
		if secret == "" {
			return s
		}
		return strings.ReplaceAll(s, secret, "[redacted]")
	}
	ae.Code, ae.Title, ae.Detail, ae.RequestID = redact(ae.Code), redact(ae.Title), redact(ae.Detail), redact(ae.RequestID)
	return ae
}

func looksJSON(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "json")
}
