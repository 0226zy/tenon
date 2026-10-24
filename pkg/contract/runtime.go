// Package contract contains the versioned cross-process DTOs shared by control,
// wsd and the dsh-go runtime. Field changes must remain backward compatible.
package contract

import "time"

const (
	RuntimeAPI = "dshd/1"
	WSDAPI     = "wsd/1"
)

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SessionView struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Cwd         string `json:"cwd"`
	Mode        string `json:"mode"`
	Model       string `json:"model"`
	CreatedAt   string `json:"created_at"`
	Running     bool   `json:"running"`
	Epoch       string `json:"epoch"`
	NextSeq     int    `json:"next_seq"`
	ActiveRunID string `json:"active_run_id,omitempty"`
}

type PrepareRunRequest struct {
	RunID       string `json:"run_id"`
	Content     string `json:"content"`
	Model       string `json:"model"`
	PayloadHash string `json:"payload_hash"`
	Generation  int64  `json:"generation"`
}

type RuntimeRunStatus string

const (
	RuntimePrepared    RuntimeRunStatus = "prepared"
	RuntimeRunning     RuntimeRunStatus = "running"
	RuntimeCancelling  RuntimeRunStatus = "cancelling"
	RuntimeStopped     RuntimeRunStatus = "stopped"
)

type RunState struct {
	RunID            string           `json:"run_id"`
	Status           RuntimeRunStatus `json:"status"`
	Outcome          string           `json:"outcome"`
	StoppedConfirmed bool             `json:"stopped_confirmed"`
	Epoch            string           `json:"epoch"`
	SeqStart         int              `json:"seq_start"`
	NextSeq          int              `json:"next_seq"`
	ErrorCode        string           `json:"error_code,omitempty"`
	Iterations       int              `json:"iterations"`
	PromptTokens     int              `json:"prompt_tokens"`
	CompletionTokens int              `json:"completion_tokens"`
	Model            string           `json:"model"`
	CreatedAt        string           `json:"created_at"`
	StartedAt        string           `json:"started_at,omitempty"`
	StoppedAt        string           `json:"stopped_at,omitempty"`
}

type RuntimeEvent struct {
	Seq     *int            `json:"seq,omitempty"`
	TS      time.Time       `json:"ts"`
	Kind    string          `json:"kind"`
	RunID   string          `json:"run_id,omitempty"`
	Message *RuntimeMessage `json:"message,omitempty"`
	Note    string          `json:"note,omitempty"`
	Marker  *RuntimeMarker  `json:"marker,omitempty"`
	Delta   string          `json:"delta,omitempty"`
}

type RuntimeMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
	IsError    bool              `json:"is_error,omitempty"`
	ToolCalls  []RuntimeToolCall `json:"tool_calls,omitempty"`
}

type RuntimeToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type RuntimeMarker struct {
	Type             string `json:"type"`
	RunID            string `json:"run_id,omitempty"`
	Detail           string `json:"detail,omitempty"`
	Iterations       int    `json:"iterations,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	Model            string `json:"model,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
}

type EventsPage struct {
	Events  []RuntimeEvent `json:"events"`
	NextSeq int            `json:"next_seq"`
}
