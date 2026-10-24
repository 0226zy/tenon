// Package cpclient calls the control plane's sandbox-internal API.
package cpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0226zy/proto-platform/pkg/contract"
)

var ErrBuildBusy = errors.New("build slots are busy")

type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("control plane returned %d: %s", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("control plane returned %d", e.StatusCode)
}

func (e *HTTPError) Is(target error) bool {
	return target == ErrBuildBusy && e.StatusCode == http.StatusTooManyRequests
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	sleep   func(context.Context, time.Duration) error
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), token: token,
		http:  &http.Client{Timeout: 10 * time.Second},
		sleep: sleepContext,
	}
}

func (c *Client) SandboxConfig(ctx context.Context) (contract.SandboxConfig, error) {
	var cfg contract.SandboxConfig
	err := c.doJSON(ctx, http.MethodGet, "/internal/sandbox/config", nil, &cfg)
	return cfg, err
}

func (c *Client) AcquireBuild(ctx context.Context, purpose string) (string, time.Duration, error) {
	if purpose == "" {
		purpose = "preview"
	}
	var result contract.BuildAcquireResult
	retryAfter, err := c.doJSONAllowBusy(ctx, http.MethodPost, "/internal/build/acquire", contract.BuildAcquireRequest{Purpose: purpose}, &result)
	return result.SlotID, retryAfter, err
}

func (c *Client) ReleaseBuild(ctx context.Context, slotID string) error {
	if slotID == "" {
		return nil
	}
	return c.doJSON(ctx, http.MethodPost, "/internal/build/release", contract.BuildReleaseRequest{SlotID: slotID}, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	_, err := c.doJSONAllowBusy(ctx, method, path, body, out)
	return err
}

func (c *Client) doJSONAllowBusy(ctx context.Context, method, path string, body, out any) (time.Duration, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	backoffs := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for attempt := 0; ; attempt++ {
		retryAfter, retry, err := c.doOnce(ctx, method, path, payload, out)
		if err == nil || !retry || attempt >= len(backoffs) {
			return retryAfter, err
		}
		if sleepErr := c.sleep(ctx, backoffs[attempt]); sleepErr != nil {
			return 0, sleepErr
		}
	}
}

// doOnce returns retry=true only for transport errors. HTTP status failures are
// deliberately left to the caller (startup retry or build-slot Retry-After).
func (c *Client) doOnce(ctx context.Context, method, path string, payload []byte, out any) (retryAfter time.Duration, retry bool, err error) {
	if c == nil || c.baseURL == "" || c.token == "" {
		return 0, false, errors.New("control plane client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return parseRetryAfter(resp.Header.Get("Retry-After")), false, decodeHTTPError(resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, false, decodeHTTPError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return 0, false, nil
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	if err := decoder.Decode(out); err != nil {
		return 0, false, fmt.Errorf("decode control plane response: %w", err)
	}
	return 0, false, nil
}

func decodeHTTPError(resp *http.Response) error {
	result := &HTTPError{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	var body contract.ErrorResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err == nil {
		result.Code = body.Error.Code
		if body.Error.Message != "" {
			result.Message = body.Error.Message
		}
	}
	return result
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Second
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
		return 0
	}
	return time.Second
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
