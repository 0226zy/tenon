// Package agentclient implements the authenticated wsd and runtime HTTP contracts.
package agentclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/pkg/contract"
)

type Error struct {
	Status             int
	Code               string
	Retryable, Unknown bool
	Err                error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("wsd status %d: %s", e.Status, e.Code)
}
func (e *Error) Unwrap() error { return e.Err }

type Client struct {
	cfg        config.AgentClient
	http       *http.Client
	streamHTTP *http.Client
}

func New(cfg config.AgentClient) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = cfg.MaxIdleConnsPerHost
	return &Client{cfg: cfg, http: &http.Client{Transport: tr}, streamHTTP: &http.Client{Transport: tr.Clone()}}
}

type CreateSessionRequest struct{ Cwd, Mode, Title, Model string }

func (c *Client) json(ctx context.Context, h sandbox.Handle, method, path string, body, out any, timeout time.Duration) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	if timeout <= 0 {
		timeout = c.cfg.Timeout.Duration
	}
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, h.BaseURL()+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.WSDToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Retryable: true, Unknown: errors.Is(err, context.DeadlineExceeded) || isNet(err), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
			return &Error{Retryable: true, Unknown: true, Err: err}
		}
	}
	return nil
}
func decodeError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var v contract.ErrorResponse
	_ = json.Unmarshal(raw, &v)
	code := v.Error.Code
	if code == "" {
		code = strings.TrimSpace(string(raw))
	}
	return &Error{Status: resp.StatusCode, Code: code, Retryable: resp.StatusCode == 429 || resp.StatusCode >= 500}
}
func isNet(err error) bool { var n net.Error; return errors.As(err, &n) }
func (c *Client) Health(ctx context.Context, h sandbox.Handle) (contract.Health, error) {
	var v contract.Health
	err := c.json(ctx, h, "GET", "/healthz", nil, &v, 0)
	return v, err
}
func (c *Client) CreateSession(ctx context.Context, h sandbox.Handle, req CreateSessionRequest) (contract.SessionView, error) {
	var v contract.SessionView
	err := c.json(ctx, h, "POST", "/agent/sessions", req, &v, 0)
	return v, err
}
func (c *Client) GetSession(ctx context.Context, h sandbox.Handle, sid string) (contract.SessionView, error) {
	var v contract.SessionView
	err := c.json(ctx, h, "GET", "/agent/sessions/"+url.PathEscape(sid), nil, &v, 0)
	return v, err
}
func (c *Client) SetModel(ctx context.Context, h sandbox.Handle, sid, model string) error {
	return c.json(ctx, h, "POST", "/agent/sessions/"+url.PathEscape(sid)+"/model", map[string]string{"model": model}, nil, 0)
}
func (c *Client) PrepareRun(ctx context.Context, h sandbox.Handle, sid string, req contract.PrepareRunRequest) (contract.RunState, error) {
	var v contract.RunState
	err := c.json(ctx, h, "POST", "/agent/sessions/"+url.PathEscape(sid)+"/runs", req, &v, 0)
	return v, err
}
func (c *Client) StartRun(ctx context.Context, h sandbox.Handle, sid, runID string) (contract.RunState, error) {
	var v contract.RunState
	err := c.json(ctx, h, "POST", "/agent/sessions/"+url.PathEscape(sid)+"/runs/"+url.PathEscape(runID)+"/start", map[string]any{}, &v, 0)
	return v, err
}
func (c *Client) GetRun(ctx context.Context, h sandbox.Handle, sid, runID string) (contract.RunState, error) {
	var v contract.RunState
	err := c.json(ctx, h, "GET", "/agent/sessions/"+url.PathEscape(sid)+"/runs/"+url.PathEscape(runID), nil, &v, 0)
	return v, err
}
func (c *Client) CancelRun(ctx context.Context, h sandbox.Handle, sid, runID string) (contract.RunState, error) {
	var v contract.RunState
	err := c.json(ctx, h, "POST", "/agent/sessions/"+url.PathEscape(sid)+"/runs/"+url.PathEscape(runID)+"/cancel", map[string]any{}, &v, 0)
	return v, err
}
func (c *Client) Events(ctx context.Context, h sandbox.Handle, sid string, from, limit int) ([]contract.RuntimeEvent, int, error) {
	if limit <= 0 {
		limit = 200
	}
	var v contract.EventsPage
	path := "/agent/sessions/" + url.PathEscape(sid) + "/events?from_seq=" + strconv.Itoa(from) + "&limit=" + strconv.Itoa(limit)
	err := c.json(ctx, h, "GET", path, nil, &v, 0)
	if v.NextSeq == 0 && len(v.Events) > 0 {
		v.NextSeq = from + len(v.Events)
	}
	return v.Events, v.NextSeq, err
}
func (c *Client) GateAcquire(ctx context.Context, h sandbox.Handle, req contract.GateAcquireRequest) (contract.GateGrant, error) {
	var v contract.GateGrant
	err := c.json(ctx, h, "POST", "/gate/acquire", req, &v, c.cfg.PreviewTimeout.Duration)
	return v, err
}
func (c *Client) GateRelease(ctx context.Context, h sandbox.Handle, runID string) error {
	return c.json(ctx, h, "POST", "/gate/release", contract.GateReleaseRequest{RunID: runID}, nil, 0)
}
func (c *Client) Reap(ctx context.Context, h sandbox.Handle, runID string) (contract.ReapResult, error) {
	var v contract.ReapResult
	err := c.json(ctx, h, "POST", "/exec/reap", contract.ReapRequest{RunID: runID}, &v, 30*time.Second)
	return v, err
}
func (c *Client) Tree(ctx context.Context, h sandbox.Handle, path string, depth int) ([]contract.FileEntry, error) {
	var v []contract.FileEntry
	err := c.json(ctx, h, "GET", "/fs/tree?path="+url.QueryEscape(path)+"&depth="+strconv.Itoa(depth), nil, &v, 0)
	return v, err
}
func (c *Client) File(ctx context.Context, h sandbox.Handle, path string, max int64) (contract.FileContent, error) {
	var v contract.FileContent
	err := c.json(ctx, h, "GET", "/fs/file?path="+url.QueryEscape(path)+"&max="+strconv.FormatInt(max, 10), nil, &v, 0)
	return v, err
}
func (c *Client) PreviewEnsure(ctx context.Context, h sandbox.Handle, req contract.PreviewEnsureRequest) (contract.PreviewState, error) {
	var v contract.PreviewState
	wait := int(c.cfg.PreviewTimeout.Duration.Seconds()) - 10
	if wait < 1 {
		wait = 1
	}
	err := c.json(ctx, h, "POST", "/preview/ensure?wait="+strconv.Itoa(wait), req, &v, c.cfg.PreviewTimeout.Duration)
	return v, err
}
func (c *Client) PreviewRestart(ctx context.Context, h sandbox.Handle) error {
	return c.json(ctx, h, "POST", "/preview/restart", map[string]any{}, nil, 0)
}
func (c *Client) PreviewStatus(ctx context.Context, h sandbox.Handle) (contract.PreviewState, error) {
	var v contract.PreviewState
	err := c.json(ctx, h, "GET", "/preview/status", nil, &v, 0)
	return v, err
}
func (c *Client) PreviewLogs(ctx context.Context, h sandbox.Handle, tail int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout.Duration)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", h.BaseURL()+"/preview/logs?tail="+strconv.Itoa(tail), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.WSDToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeError(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
func (c *Client) Commit(ctx context.Context, h sandbox.Handle, req contract.CommitRequest) (contract.CommitResult, error) {
	var v contract.CommitResult
	err := c.json(ctx, h, "POST", "/git/commit", req, &v, 60*time.Second)
	return v, err
}

type RawStream struct {
	resp   *http.Response
	reader *bufio.Reader
}

func (c *Client) Stream(ctx context.Context, h sandbox.Handle, sid string, from int) (*RawStream, error) {
	return c.stream(ctx, h, "/agent/sessions/"+url.PathEscape(sid)+"/stream?from_seq="+strconv.Itoa(from))
}
func (c *Client) stream(ctx context.Context, h sandbox.Handle, path string) (*RawStream, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", h.BaseURL()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.WSDToken)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.streamHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return &RawStream{resp: resp, reader: bufio.NewReader(resp.Body)}, nil
}
func (s *RawStream) Close() error { return s.resp.Body.Close() }
func (s *RawStream) Next() (contract.RuntimeEvent, error) {
	var out contract.RuntimeEvent
	var data strings.Builder
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return out, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			raw := strings.TrimSuffix(data.String(), "\n")
			if err := json.Unmarshal([]byte(raw), &out); err != nil {
				return out, err
			}
			return out, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
		}
	}
}

type WSDEvent struct {
	Type       string                `json:"type"`
	Paths      []string              `json:"paths"`
	Preview    contract.PreviewState `json:"preview"`
	Status     string                `json:"status"`
	Generation int64                 `json:"generation"`
	Commit     string                `json:"commit"`
	UsedBytes  int64                 `json:"used_bytes"`
	LimitBytes int64                 `json:"limit_bytes"`
}
type WSDStream struct{ *RawStream }

func (c *Client) WSDEvents(ctx context.Context, h sandbox.Handle) (*WSDStream, error) {
	s, err := c.stream(ctx, h, "/events")
	if err != nil {
		return nil, err
	}
	return &WSDStream{s}, nil
}
func (s *WSDStream) NextWSD() (WSDEvent, error) {
	var out WSDEvent
	var data strings.Builder
	eventName := ""
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return out, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if data.Len() == 0 {
				eventName = ""
				continue
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(data.String())), &out); err != nil {
				return out, err
			}
			if out.Type == "" {
				out.Type = eventName
			}
			return out, nil
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			data.WriteByte('\n')
		}
	}
}
