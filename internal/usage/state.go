package usage

// Phase is what an agent is doing, read from its log.
type Phase string

const (
	Idle       Phase = "idle"
	Generating Phase = "generating" // a request is in flight
	Tools      Phase = "tools"      // running tools between requests
)

// AgentState is one log file's latest view of its agent.
type AgentState struct {
	Agent      string `json:"agent"`
	Source     Source `json:"source"`
	Project    string `json:"project,omitempty"`
	Model      string `json:"model,omitempty"`
	Subagent   bool   `json:"subagent,omitempty"`
	Phase      Phase  `json:"phase"`
	InFlightMs int64  `json:"inFlightMs,omitempty"` // when the request in flight started; 0 unless generating
	LastLineMs int64  `json:"lastLineMs"`
	// Context is the session's context now: the input of its latest
	// request, cache reads and writes included. ContextWindow is the
	// model's limit, 0 when not known.
	Context       int64 `json:"context,omitempty"`
	ContextWindow int64 `json:"contextWindow,omitempty"`
}

// QuotaWindow is one rate-limit window of a subscription.
type QuotaWindow struct {
	Kind          string  `json:"kind"` // fiveHour, sevenDay or other
	UsedPercent   float64 `json:"usedPercent"`
	WindowMinutes int64   `json:"windowMinutes"`
	ResetsAtMs    int64   `json:"resetsAtMs,omitempty"` // 0 when unknown
	// Pace compares the use with the time gone in the window; set when a
	// snapshot is made, nil when it can't be told
	Pace *Pace `json:"pace,omitempty"`
}

// Pace is how fast a quota window is being used against the time that has
// gone in it.
type Pace struct {
	// Stage: onTrack, slightlyAhead, ahead, farAhead (using faster than
	// time goes) or slightlyBehind, behind, farBehind
	Stage           string  `json:"stage"`
	ExpectedPercent float64 `json:"expectedPercent"` // the share of the window gone
	DeltaPercent    float64 `json:"deltaPercent"`    // used minus expected
	// RunsOutInMs is the time until 100 % at the window's pace so far, when
	// that comes before the reset; 0 otherwise
	RunsOutInMs int64 `json:"runsOutInMs,omitempty"`
	// LastsToReset: at this pace the window resets before it runs out
	LastsToReset bool `json:"lastsToReset"`
}

// Quota is the newest limits snapshot a source reported.
type Quota struct {
	Source     Source        `json:"source"`
	Plan       string        `json:"plan,omitempty"`
	ObservedMs int64         `json:"observedMs"`
	Windows    []QuotaWindow `json:"windows"`
	Reached    string        `json:"reached,omitempty"` // which limit was hit, if any
}
