// Package httpx contains the shared HTTP error and JSON conventions.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/0226zy/proto-platform/pkg/contract"
)

type Error struct {
	Status  int
	Code    string
	Message string
	Cause   error
	Header  http.Header
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Cause)
	}
	return e.Code + ": " + e.Message
}
func (e *Error) Unwrap() error { return e.Cause }

func E(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func WriteError(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = E(http.StatusInternalServerError, "INTERNAL", "internal error")
	}
	for k, values := range e.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	JSON(w, e.Status, contract.ErrorResponse{Error: contract.ErrorBody{Code: e.Code, Message: e.Message}})
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return E(http.StatusBadRequest, "BAD_REQUEST", "invalid JSON request")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return E(http.StatusBadRequest, "BAD_REQUEST", "request must contain one JSON object")
	}
	return nil
}

func Bearer(r *http.Request) string {
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}
