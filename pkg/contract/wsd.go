package contract

import "time"

type Health struct {
	API       string `json:"api"`
	Agent     string `json:"agent,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Image     string `json:"image,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	Version   string `json:"version,omitempty"`
}

type GateAcquireRequest struct {
	Holder     string `json:"holder"`
	RunID      string `json:"run_id,omitempty"`
	Generation int64  `json:"generation"`
	WaitMS     int    `json:"wait_ms,omitempty"`
}

type GateGrant struct {
	Holder     string    `json:"holder"`
	RunID      string    `json:"run_id,omitempty"`
	Generation int64     `json:"generation"`
	Since      time.Time `json:"since"`
}

type GateReleaseRequest struct {
	RunID string `json:"run_id"`
}

type GateState struct {
	Held          bool       `json:"held"`
	Holder        string     `json:"holder,omitempty"`
	RunID         string     `json:"run_id,omitempty"`
	Generation    int64      `json:"generation"`
	MaxGeneration int64      `json:"max_generation"`
	Since         *time.Time `json:"since,omitempty"`
}

type ReapRequest struct {
	RunID string `json:"run_id"`
}

type ReapResult struct {
	Killed    int `json:"killed"`
	Remaining int `json:"remaining"`
}

type FileEntry struct {
	Name  string    `json:"name"`
	Path  string    `json:"path"`
	Type  string    `json:"type"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

type FileContent struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content,omitempty"`
	ETag      string `json:"etag"`
}

type PreviewState struct {
	Status     string `json:"status"`
	Generation int64  `json:"generation"`
	Commit     string `json:"commit,omitempty"`
	URLPath    string `json:"url_path"`
	Probe      struct {
		Code int   `json:"code"`
		MS   int64 `json:"ms"`
	} `json:"probe"`
	LogTail   string `json:"log_tail,omitempty"`
	Restarts  int    `json:"restarts,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	// LocalPort is used only by the explicitly trusted local driver. Container
	// deployments always proxy to the fixed container address on port 3000.
	LocalPort int `json:"local_port,omitempty"`
}

type PreviewEnsureRequest struct {
	Generation int64  `json:"generation"`
	Commit     string `json:"commit,omitempty"`
}

type PreviewEnsureResponse = PreviewState

type FileChange struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

type CommitRequest struct {
	RunID      string `json:"run_id"`
	Generation int64  `json:"generation"`
	Message    string `json:"message"`
}

type CommitResult struct {
	Commit           string       `json:"commit"`
	Changed          bool         `json:"changed"`
	Files            []FileChange `json:"files"`
	FilesTotal       int          `json:"files_total"`
	CheckpointStatus string       `json:"checkpoint_status"`
	Error            string       `json:"error,omitempty"`
}

type GitLogEntry struct {
	Commit  string    `json:"commit"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
	RunID   string    `json:"run_id,omitempty"`
}

type SandboxModel struct {
	ID      string `json:"id"`
	Default bool   `json:"default"`
}

type SandboxConfig struct {
	Models []SandboxModel `json:"models"`
	Agent  struct {
		MaxIterations          int `json:"max_iterations"`
		TokenBudgetPerTurn     int `json:"token_budget_per_turn"`
		ContextCompressAtTokens int `json:"context_compress_at_tokens"`
	} `json:"agent"`
	SystemPromptAppend string `json:"system_prompt_append,omitempty"`
	Preview            struct {
		Mode          string `json:"mode"`
		BuildTimeoutS int    `json:"build_timeout_s"`
		MaxRestarts   int    `json:"max_restarts"`
		LogLines      int    `json:"log_lines"`
	} `json:"preview"`
	Disk struct {
		LimitBytes    int64   `json:"limit_bytes"`
		HighWatermark float64 `json:"high_watermark"`
	} `json:"disk"`
}

type BuildAcquireRequest struct {
	Purpose string `json:"purpose"`
}

type BuildAcquireResult struct {
	SlotID string `json:"slot_id"`
}

type BuildReleaseRequest struct {
	SlotID string `json:"slot_id"`
}
