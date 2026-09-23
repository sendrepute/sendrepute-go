package sendrepute

// Model identifies a supported SendRepute classifier family.
type Model string

const (
	ModelThor   Model = "thor"
	ModelTheos  Model = "theos"
	ModelAthena Model = "athena"
	ModelOdin   Model = "odin"
	ModelFreya  Model = "freya"
	ModelHermes Model = "hermes"
	ModelAres   Model = "ares"
	ModelApollo Model = "apollo"
)

// ClassificationInput contains only the three message content fields accepted
// by the API. It has no recipient or attachment fields.
type ClassificationInput struct {
	Sender  string
	Subject string
	Body    string
	Model   Model
}

type ClassificationResponse struct {
	RequestID string               `json:"requestId"`
	Model     Model                `json:"model"`
	Result    ClassificationResult `json:"result"`
	Billing   Billing              `json:"billing"`
}

type Billing struct {
	ChargedMillicents int64 `json:"chargedMillicents"`
	Replayed          bool  `json:"replayed"`
}

type ClassificationResult struct {
	Label            string        `json:"label"`
	SpamProbability  float64       `json:"spamProbability"`
	FlaggedTermCount *int64        `json:"flaggedTermCount,omitempty"`
	Confidence       string        `json:"confidence"`
	Reasons          []Reason      `json:"reasons"`
	FlaggedTerms     []string      `json:"flaggedTerms"`
	AnalyzedFields   []string      `json:"analyzedFields"`
	ModelVersion     string        `json:"modelVersion"`
	AnalyzedAt       string        `json:"analyzedAt"`
	ContentAudit     *ContentAudit `json:"contentAudit,omitempty"`
}

type Reason struct {
	Signal string  `json:"signal"`
	Detail string  `json:"detail"`
	Weight float64 `json:"weight"`
}

type ContentAudit struct {
	Score           int64          `json:"score"`
	Grade           string         `json:"grade"`
	Summary         string         `json:"summary"`
	Counts          AuditCounts    `json:"counts"`
	TotalIssues     int64          `json:"totalIssues"`
	CriticalCount   int64          `json:"criticalCount"`
	WarningCount    int64          `json:"warningCount"`
	SuggestionCount int64          `json:"suggestionCount"`
	Issues          []AuditIssue   `json:"issues"`
	GoodPractices   []GoodPractice `json:"goodPractices"`
	HomoglyphTerms  []string       `json:"homoglyphTerms,omitempty"`
	InputTruncated  bool           `json:"inputTruncated"`
}

type AuditCounts struct {
	Words          int64 `json:"words"`
	Links          int64 `json:"links"`
	Images         int64 `json:"images"`
	TriggerPhrases int64 `json:"triggerPhrases"`
}

type AuditIssue struct {
	Code      string `json:"code"`
	Category  string `json:"category"`
	Severity  string `json:"severity"`
	Deduction int64  `json:"deduction"`
	Evidence  string `json:"evidence"`
}

type GoodPractice struct {
	Code     string `json:"code"`
	Category string `json:"category"`
}
