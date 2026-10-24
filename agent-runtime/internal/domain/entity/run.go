package entity

// RunStatus 是 Runtime 执行状态，而非控制层产品状态。
type RunStatus string

const (
	RunPrepared   RunStatus = "prepared"
	RunRunning    RunStatus = "running"
	RunCancelling RunStatus = "cancelling"
	RunStopped    RunStatus = "stopped"
)

// PrepareRun 是 Run 的幂等接收请求。PayloadHash 由调用方对规范化请求计算，
// Runtime 用它识别同 run_id 的冲突重试，不在传输途中重新解释内容。
type PrepareRun struct {
	RunID       string `json:"run_id"`
	Content     string `json:"content"`
	Model       string `json:"model"`
	PayloadHash string `json:"payload_hash"`
	Generation  int64  `json:"generation"`
}

// RunState 是跨进程查询契约。StoppedConfirmed 只在回合 goroutine 已返回且
// 本 Run 登记的全部进程组均为空后才为 true；turn marker 从不直接驱动该字段。
type RunState struct {
	RunID            string    `json:"run_id"`
	Status           RunStatus `json:"status"`
	Outcome          string    `json:"outcome"`
	StoppedConfirmed bool      `json:"stopped_confirmed"`
	Epoch            string    `json:"epoch"`
	SeqStart         int       `json:"seq_start"`
	NextSeq          int       `json:"next_seq"`
	ErrorCode        string    `json:"error_code,omitempty"`
	Iterations       int       `json:"iterations"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	Model            string    `json:"model"`
	CreatedAt        string    `json:"created_at"`
	StartedAt        string    `json:"started_at,omitempty"`
	StoppedAt        string    `json:"stopped_at,omitempty"`
}

// RunRecord 是持久化执行记录。Content 只存在记录文件中，查询 API 仅暴露 RunState。
type RunRecord struct {
	RunState
	SessionID   string `json:"session_id"`
	Content     string `json:"content"`
	PayloadHash string `json:"payload_hash"`
	Generation  int64  `json:"generation"`
	Pgids       []int  `json:"pgids,omitempty"`
}

// Terminal 报告 Run 是否已进入 stopped。
func (r RunRecord) Terminal() bool { return r.Status == RunStopped }
