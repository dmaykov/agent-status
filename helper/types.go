package main

type window struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	AppID       string `json:"app_id"`
	PID         int    `json:"pid"`
	WorkspaceID int64  `json:"workspace_id"`
	IsFocused   bool   `json:"is_focused"`
	Layout      struct {
		PosInScrollingLayout []int `json:"pos_in_scrolling_layout"`
	} `json:"layout"`
	FocusTimestamp struct {
		Secs  int64 `json:"secs"`
		Nanos int64 `json:"nanos"`
	} `json:"focus_timestamp"`
}

type workspace struct {
	ID             int64  `json:"id"`
	Idx            int    `json:"idx"`
	Name           string `json:"name"`
	IsFocused      bool   `json:"is_focused"`
	ActiveWindowID int64  `json:"active_window_id"`
}

type procInfo struct {
	PID        int
	PPID       int
	StartTicks int64
	Cmdline    []string
	ExeName    string
	CmdText    string
	Cwd        string
}

type detectedAgent struct {
	WindowID        int64
	WindowPID       int
	WorkspaceID     int64
	Tool            string
	ToolDisplay     string
	AgentPID        int
	Cwd             string
	AgentStartEpoch float64
	WindowTitle     string
	Focused         bool
	Position        [2]int
	FocusOrder      [2]int64
}

type recoveredPrompt struct {
	ThreadID        string
	SessionID       string
	Cwd             string
	ProjectDir      string
	CreatedAt       int64
	UpdatedAt       int64
	Label           string
	PromptFirstLine string
	LastPrompt      string
	MatchKind       string
}

type finalAgent struct {
	WindowID        int64    `json:"window_id"`
	WorkspaceID     int64    `json:"workspace_id"`
	Tool            string   `json:"tool"`
	ToolDisplay     string   `json:"tool_display"`
	Label           string   `json:"label"`
	PromptFirstLine string   `json:"prompt_first_line"`
	ProjectDir      string   `json:"project_dir"`
	WindowTitle     string   `json:"window_title"`
	Focused         bool     `json:"focused"`
	Position        [2]int   `json:"position"`
	FocusOrder      [2]int64 `json:"focus_order"`
}

type workspaceState struct {
	ID                int64        `json:"id"`
	Idx               int          `json:"idx"`
	Name              string       `json:"name"`
	IsFocused         bool         `json:"is_focused"`
	ActiveWindowID    int64        `json:"active_window_id"`
	ActiveWindowTitle string       `json:"active_window_title"`
	SummaryText       string       `json:"summary_text"`
	PrimaryPromptLine string       `json:"primary_prompt_line"`
	AgentCount        int          `json:"agent_count"`
	Agents            []finalAgent `json:"agents"`
}

type state struct {
	GeneratedAt      string           `json:"generated_at"`
	FocusedWorkspace *workspaceState  `json:"focused_workspace"`
	SummaryProvider  summaryProvider  `json:"summary_provider"`
	Workspaces       []workspaceState `json:"workspaces"`
}

type summaryProvider struct {
	Selected        string `json:"selected"`
	Effective       string `json:"effective"`
	CodexAvailable  bool   `json:"codex_available"`
	ClaudeAvailable bool   `json:"claude_available"`
}

type codexLogRow struct {
	ProcessUUID string `json:"process_uuid"`
	ThreadID    string `json:"thread_id"`
	LastTS      int64  `json:"last_ts"`
}

type codexThreadRow struct {
	ID               string `json:"id"`
	Cwd              string `json:"cwd"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
	Title            string `json:"title"`
	FirstUserMessage string `json:"first_user_message"`
}

type claudeSessionCache struct {
	ModTimeNS int64
	Size      int64
	Data      recoveredPrompt
}

type summaryCacheEntry struct {
	Summary   string `json:"summary"`
	UpdatedAt int64  `json:"updated_at"`
}

type summaryRequest struct {
	CacheKey string
	Prompt   string
	Title    string
}

type helperSettings struct {
	SummaryProvider string `json:"summary_provider"`
}
