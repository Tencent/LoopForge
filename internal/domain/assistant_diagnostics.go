package domain

import "time"

// AssistantFailure is safe to show beside a reply. Native request/trace IDs
// belong to the diagnostic report; a generation ID is never called a trace ID.
type AssistantFailure struct {
	Code       string `json:"code"`
	Phase      string `json:"phase"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// AssistantDiagnostics is a bounded, sanitized record of one turn. It does
// not contain prompts, command arguments, environment or raw provider streams.
type AssistantDiagnostics struct {
	Version          int                             `json:"version"`
	GenerationID     string                          `json:"generationId"`
	SessionID        string                          `json:"sessionId"`
	Status           string                          `json:"status"`
	StartedAt        time.Time                       `json:"startedAt"`
	UpdatedAt        time.Time                       `json:"updatedAt"`
	Failure          *AssistantFailure               `json:"failure,omitempty"`
	Error            string                          `json:"error,omitempty"`
	Attempts         []AssistantCLIAttempt           `json:"attempts"`
	Truncated        bool                            `json:"truncated,omitempty"`
	Environment      *AssistantDiagnosticEnvironment `json:"environment,omitempty"`
	LastCheckpointAt *time.Time                      `json:"lastCheckpointAt,omitempty"`
	Timeline         []AssistantDiagnosticEvent      `json:"timeline,omitempty"`
	Tools            []AssistantDiagnosticTool       `json:"tools,omitempty"`
	Termination      *AssistantDiagnosticTermination `json:"termination,omitempty"`
	Assessment       *AssistantDiagnosticAssessment  `json:"assessment,omitempty"`
	Coverage         []string                        `json:"coverage,omitempty"`
	Dropped          map[string]int                  `json:"dropped,omitempty"`
}

// These facts are portable: no hostname, user identity, installation path or
// environment dump is required to identify a build and execution instance.
type AssistantDiagnosticEnvironment struct {
	PlatformVersion   string    `json:"platformVersion"`
	Revision          string    `json:"revision,omitempty"`
	Modified          *bool     `json:"modified,omitempty"`
	GoVersion         string    `json:"goVersion"`
	OS                string    `json:"os"`
	Arch              string    `json:"arch"`
	InstanceID        string    `json:"instanceId"`
	InstanceStartedAt time.Time `json:"instanceStartedAt"`
}

type AssistantDiagnosticTermination struct {
	At      time.Time `json:"at"`
	Code    string    `json:"code"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

// Assessment describes observed evidence, never an invented provider root cause.
type AssistantDiagnosticAssessment struct {
	Outcome   string   `json:"outcome"`
	Summary   string   `json:"summary"`
	Evidence  []string `json:"evidence,omitempty"`
	Unknowns  []string `json:"unknowns,omitempty"`
	NextSteps []string `json:"nextSteps,omitempty"`
}

type AssistantDiagnosticProcess struct {
	PID           int        `json:"pid,omitempty"`
	State         string     `json:"state"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Signal        string     `json:"signal,omitempty"`
	UserCPUMs     int64      `json:"userCpuMs,omitempty"`
	SystemCPUMs   int64      `json:"systemCpuMs,omitempty"`
	StdoutLines   int        `json:"stdoutLines"`
	StderrLines   int        `json:"stderrLines"`
	FirstStdoutAt *time.Time `json:"firstStdoutAt,omitempty"`
	LastStdoutAt  *time.Time `json:"lastStdoutAt,omitempty"`
	FirstStderrAt *time.Time `json:"firstStderrAt,omitempty"`
	LastStderrAt  *time.Time `json:"lastStderrAt,omitempty"`
}

type AssistantDiagnosticTool struct {
	ID         string     `json:"id"`
	AttemptID  string     `json:"attemptId,omitempty"`
	Source     string     `json:"source"`
	Name       string     `json:"name"`
	ParentID   string     `json:"parentId,omitempty"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      string     `json:"error,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	StatusCode int        `json:"statusCode,omitempty"`
	RequestID  string     `json:"requestId,omitempty"`
	TraceID    string     `json:"traceId,omitempty"`
}

type AssistantCLIAttempt struct {
	ID              string                      `json:"id"`
	Engine          string                      `json:"engine"`
	Model           string                      `json:"model,omitempty"`
	Resume          bool                        `json:"resume"`
	StartedAt       time.Time                   `json:"startedAt"`
	FinishedAt      *time.Time                  `json:"finishedAt,omitempty"`
	Phase           string                      `json:"phase"`
	Status          string                      `json:"status"`
	ExitCode        *int                        `json:"exitCode,omitempty"`
	Error           string                      `json:"error,omitempty"`
	Stderr          []string                    `json:"stderr,omitempty"`
	Events          []AssistantDiagnosticEvent  `json:"events,omitempty"`
	CLIVersion      string                      `json:"cliVersion,omitempty"`
	VersionSource   string                      `json:"versionSource,omitempty"`
	BinarySource    string                      `json:"binarySource,omitempty"`
	Endpoint        string                      `json:"endpoint,omitempty"`
	Protocol        string                      `json:"protocol,omitempty"`
	Effort          string                      `json:"effort,omitempty"`
	Process         *AssistantDiagnosticProcess `json:"process,omitempty"`
	FirstStderr     []string                    `json:"firstStderr,omitempty"`
	FirstError      *AssistantDiagnosticEvent   `json:"firstError,omitempty"`
	NativeSessionID string                      `json:"nativeSessionId,omitempty"`
	NativeTurnID    string                      `json:"nativeTurnId,omitempty"`
}

type AssistantDiagnosticEvent struct {
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	Message    string    `json:"message,omitempty"`
	Code       string    `json:"code,omitempty"`
	Phase      string    `json:"phase,omitempty"`
	RequestID  string    `json:"requestId,omitempty"`
	TraceID    string    `json:"traceId,omitempty"`
	StatusCode int       `json:"statusCode,omitempty"`
	Retryable  *bool     `json:"retryable,omitempty"`
	Sequence   int       `json:"sequence,omitempty"`
	Source     string    `json:"source,omitempty"`
	AttemptID  string    `json:"attemptId,omitempty"`
	ToolID     string    `json:"toolId,omitempty"`
	Tool       string    `json:"tool,omitempty"`
}
