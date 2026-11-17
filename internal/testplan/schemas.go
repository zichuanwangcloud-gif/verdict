package testplan

import "encoding/json"

// 各阶段的结构化输出 Schema。两种 harness 共用：Codex 要求 additionalProperties=false
// 且全部字段 required，所以可选内容一律用空字符串 / 空数组表示。

func obj(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": req, "properties": props}
}

func arr(items any) map[string]any { return map[string]any{"type": "array", "items": items} }

func enum(vals ...string) map[string]any { return map[string]any{"type": "string", "enum": vals} }

var (
	str     = map[string]any{"type": "string"}
	boolean = map[string]any{"type": "boolean"}
)

func mustSchema(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

var finding = obj(map[string]any{
	"severity": enum("critical", "high", "medium", "low"),
	"title":    str,
	"detail":   str,
	"evidence": str,
})

var reportAC = obj(map[string]any{
	"id":         str,
	"criterion":  str,
	"status":     enum("pass", "fail", "unverifiable"),
	"evidence":   str,
	"reasoning":  str,
	"confidence": enum("high", "medium", "low"),
	"response":   str,
})

var (
	PlanSchema = mustSchema(obj(map[string]any{
		"summary":       str,
		"needs_service": boolean,
		"service": obj(map[string]any{
			"setup_cmd":  str,
			"start_cmd":  str,
			"health_url": str,
			"app_url":    str,
		}),
		"items": arr(obj(map[string]any{
			"id":            str,
			"criterion":     str,
			"method":        enum("cli", "api", "ui", "code"),
			"preconditions": str,
			"steps":         arr(str),
			"expected":      str,
		})),
	}))

	ReportSchema = mustSchema(obj(map[string]any{
		"summary": str,
		"acs":     arr(reportAC),
	}))

	UISchema = mustSchema(obj(map[string]any{
		"summary": str,
		"pages": arr(obj(map[string]any{
			"url":          str,
			"title":        str,
			"screenshots":  arr(str),
			"observations": str,
		})),
		"acs":      arr(reportAC),
		"findings": arr(finding),
	}))

	RedSchema = mustSchema(obj(map[string]any{
		"summary": str,
		"challenges": arr(obj(map[string]any{
			"ac_id":           str,
			"kind":            enum("insufficient_evidence", "counterexample", "wrong_status", "flaky", "other"),
			"claim":           str,
			"suggested_check": str,
			"proposed_status": enum("pass", "fail", "unverifiable"),
		})),
		"findings": arr(finding),
	}))

	ExploreSchema = mustSchema(obj(map[string]any{
		"summary":  str,
		"findings": arr(finding),
	}))

	JudgeSchema = mustSchema(obj(map[string]any{
		"summary": str,
		"acs": arr(obj(map[string]any{
			"id":         str,
			"status":     enum("pass", "fail", "unverifiable", "disputed"),
			"confidence": enum("high", "medium", "low"),
			"rationale":  str,
		})),
	}))
)

// ---- 解析用的 Go 结构 ----

type ServiceHint struct {
	SetupCmd  string `json:"setup_cmd"`
	StartCmd  string `json:"start_cmd"`
	HealthURL string `json:"health_url"`
	AppURL    string `json:"app_url"`
}

type PlanItem struct {
	ID            string   `json:"id"`
	Criterion     string   `json:"criterion"`
	Method        string   `json:"method"`
	Preconditions string   `json:"preconditions"`
	Steps         []string `json:"steps"`
	Expected      string   `json:"expected"`
}

type Plan struct {
	Summary      string      `json:"summary"`
	NeedsService bool        `json:"needs_service"`
	Service      ServiceHint `json:"service"`
	Items        []PlanItem  `json:"items"`
}

type reportOut struct {
	Summary string `json:"summary"`
	ACs     []struct {
		ID         string `json:"id"`
		Criterion  string `json:"criterion"`
		Status     string `json:"status"`
		Evidence   string `json:"evidence"`
		Reasoning  string `json:"reasoning"`
		Confidence string `json:"confidence"`
		Response   string `json:"response"`
	} `json:"acs"`
}

type findingOut struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Evidence string `json:"evidence"`
}

type UIPage struct {
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	Screenshots  []string `json:"screenshots"`
	Observations string   `json:"observations"`
}

type uiOut struct {
	reportOut
	Pages    []UIPage     `json:"pages"`
	Findings []findingOut `json:"findings"`
}

type challengeOut struct {
	ACID           string `json:"ac_id"`
	Kind           string `json:"kind"`
	Claim          string `json:"claim"`
	SuggestedCheck string `json:"suggested_check"`
	ProposedStatus string `json:"proposed_status"`
}

type redOut struct {
	Summary    string         `json:"summary"`
	Challenges []challengeOut `json:"challenges"`
	Findings   []findingOut   `json:"findings"`
}

type exploreOut struct {
	Summary  string       `json:"summary"`
	Findings []findingOut `json:"findings"`
}

type judgeOut struct {
	Summary string `json:"summary"`
	ACs     []struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		Confidence string `json:"confidence"`
		Rationale  string `json:"rationale"`
	} `json:"acs"`
}
