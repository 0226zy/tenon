package contract

import (
	"encoding/json"
	"time"
)

type RunStatus string

const (
	RunQueued      RunStatus = "queued"
	RunDispatching RunStatus = "dispatching"
	RunRunning     RunStatus = "running"
	RunCancelling  RunStatus = "cancelling"
	RunFinalizing  RunStatus = "finalizing"
	RunFinished    RunStatus = "finished"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
)

func (s RunStatus) Active() bool {
	switch s {
	case RunQueued, RunDispatching, RunRunning, RunCancelling, RunFinalizing:
		return true
	default:
		return false
	}
}

func (s RunStatus) Code() int {
	switch s {
	case RunQueued:
		return 0
	case RunRunning:
		return 1
	case RunFinished:
		return 2
	case RunFailed:
		return 3
	case RunCancelled:
		return 4
	case RunDispatching:
		return 5
	case RunCancelling:
		return 6
	case RunFinalizing:
		return 7
	default:
		return -1
	}
}

func RunStatusFromCode(code int) RunStatus {
	switch code {
	case 0:
		return RunQueued
	case 1:
		return RunRunning
	case 2:
		return RunFinished
	case 3:
		return RunFailed
	case 4:
		return RunCancelled
	case 5:
		return RunDispatching
	case 6:
		return RunCancelling
	case 7:
		return RunFinalizing
	default:
		return RunFailed
	}
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type UserView struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

type ModelView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Tags    []string `json:"tags"`
	Desc    string   `json:"desc"`
	Default bool     `json:"default"`
}

type RunBrief struct {
	RunID  string    `json:"run_id"`
	Status RunStatus `json:"status"`
}

type PreviewBrief struct {
	Status     string `json:"status"`
	Commit     string `json:"commit,omitempty"`
	Generation int64  `json:"generation"`
}

type ConversationView struct {
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Model      string        `json:"model"`
	Status     string        `json:"status"`
	LastActive string        `json:"last_active"`
	CreatedAt  string        `json:"created_at"`
	Run        *RunBrief     `json:"run,omitempty"`
	Preview    *PreviewBrief `json:"preview,omitempty"`
}

type ConversationDetail struct {
	ConversationView
	Sandbox struct {
		Status string `json:"status"`
	} `json:"sandbox"`
	Preview   PreviewBrief `json:"preview"`
	LastSeq   int64        `json:"last_seq"`
	ActiveRun *RunBrief    `json:"active_run,omitempty"`
}

type CreateConversationRequest struct {
	Title    string `json:"title,omitempty"`
	Model    string `json:"model,omitempty"`
	Template string `json:"template,omitempty"`
}

type UpdateConversationRequest struct {
	Title *string `json:"title,omitempty"`
	Model *string `json:"model,omitempty"`
}

type AcceptRequest struct {
	ClientRequestID string `json:"client_request_id"`
	Content         string `json:"content"`
	Model           string `json:"model,omitempty"`
}

type AcceptResult struct {
	RunID     string    `json:"run_id"`
	MessageID string    `json:"message_id"`
	Seq       int64     `json:"seq"`
	Status    RunStatus `json:"status"`
}

type MessageView struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	RunID     string `json:"run_id"`
	CreatedAt string `json:"created_at"`
}

type Usage struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
}

type RunView struct {
	RunID            string    `json:"run_id"`
	Status           RunStatus `json:"status"`
	CheckpointStatus string    `json:"checkpoint_status"`
	Commit           string    `json:"commit,omitempty"`
	ErrorCode        string    `json:"error_code,omitempty"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	Generation       int64     `json:"generation"`
	Usage            Usage     `json:"usage"`
	StartedAt        string    `json:"started_at,omitempty"`
	EndedAt          string    `json:"ended_at,omitempty"`
}

type RunEnd struct {
	Commit           string       `json:"commit,omitempty"`
	CheckpointStatus string       `json:"checkpoint_status"`
	Files            []FileChange `json:"files"`
	FilesTotal       int          `json:"files_total"`
	DurationMS       int64        `json:"duration_ms"`
	Usage            Usage        `json:"usage"`
	ErrorCode        string       `json:"error_code,omitempty"`
}

type PreviewView struct {
	Status     string `json:"status"`
	URL        string `json:"url,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Generation int64  `json:"generation"`
	Stale      bool   `json:"stale"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

type EventRecord struct {
	ConvID    string          `json:"conv_id"`
	Seq       int64           `json:"seq"`
	RunID     string          `json:"run_id,omitempty"`
	Source    int             `json:"source"`
	RtEpoch   string          `json:"runtime_epoch,omitempty"`
	RtSession string          `json:"agent_session_id,omitempty"`
	RtSeq     *int            `json:"rt_seq,omitempty"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type UIItem struct {
	Key  string         `json:"key"`
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type UIFrame struct {
	Seq   *int64   `json:"seq,omitempty"`
	RunID string   `json:"run_id,omitempty"`
	TS    string   `json:"ts"`
	Items []UIItem `json:"items"`
}
