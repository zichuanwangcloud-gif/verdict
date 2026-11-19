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

// shot 是一张截图的登记信息：它代表哪个界面状态、用来证明哪条验收标准、画面是否可用。
var shot = obj(map[string]any{
	"file":     str,
	"url":      str,
	"state":    str,
	"purpose":  str,
	"covered":  boolean, // 画面是否被遮挡
	"readable": boolean, // 内容是否清晰可辨
	"issues":   arr(str),
})

var (
	PlanSchema = mustSchema(obj(map[string]any{
		"summary":       str,
		"needs_service": boolean,
		"service": obj(map[string]any{
			"setup_cmd":    str,
			"start_cmd":    str,
			"health_url":   str,
			"app_url":      str,
			"teardown_cmd": str,
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
		"shots":    arr(shot),
		"acs":      arr(reportAC),
		"findings": arr(finding),
	}))

	// ShotsSchema 是独立截图质检阶段的输出：只判断每张图能不能当证据用。
	ShotsSchema = mustSchema(obj(map[string]any{
		"summary": str,
		"shot_reviews": arr(obj(map[string]any{
			"file":     str,
			"verdict":  enum("usable", "unusable"),
			"occluded": boolean,
			"readable": boolean,
			"problems": arr(str),
			"suggest":  str,
		})),
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
		"findings": arr(finding),
	}))
)

// ---- 解析用的 Go 结构 ----

type ServiceHint struct {
	SetupCmd  string `json:"setup_cmd"`
	StartCmd  string `json:"start_cmd"`
	HealthURL string `json:"health_url"`
	AppURL    string `json:"app_url"`
	// TeardownCmd 在测试收尾时执行，删除本次在共享容器里建的库、清空占用的 redis 编号等。
	TeardownCmd string `json:"teardown_cmd"`
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

// Shot 是一张截图的登记信息。Covered / Readable 由 UI 阶段自检后填写，
// 独立质检阶段的结论另见 ShotReview。
type Shot struct {
	File     string   `json:"file"`
	URL      string   `json:"url"`
	State    string   `json:"state"`    // 这张图对应的界面状态，如「弹窗打开」「表单报错」
	Purpose  string   `json:"purpose"`  // 用来证明哪条验收标准
	Covered  bool     `json:"covered"`  // 画面是否被遮挡
	Readable bool     `json:"readable"` // 内容是否清晰可辨
	Issues   []string `json:"issues"`
}

// ShotReview 是独立质检阶段对一张截图的判定。
type ShotReview struct {
	File     string   `json:"file"`
	Verdict  string   `json:"verdict"` // usable / unusable
	Occluded bool     `json:"occluded"`
	Readable bool     `json:"readable"`
	Problems []string `json:"problems"`
	Suggest  string   `json:"suggest"`
	Source   string   `json:"source,omitempty"` // 判定来自哪个阶段（self / shots / machine）
}

type uiOut struct {
	reportOut
	Pages    []UIPage     `json:"pages"`
	Shots    []Shot       `json:"shots"`
	Findings []findingOut `json:"findings"`
}

type shotReviewOut struct {
	File     string   `json:"file"`
	Verdict  string   `json:"verdict"`
	Occluded bool     `json:"occluded"`
	Readable bool     `json:"readable"`
	Problems []string `json:"problems"`
	Suggest  string   `json:"suggest"`
}

type shotsOut struct {
	Summary string          `json:"summary"`
	Reviews []shotReviewOut `json:"shot_reviews"`
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
	Summary  string       `json:"summary"`
	Findings []findingOut `json:"findings"`
	ACs      []struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		Confidence string `json:"confidence"`
		Rationale  string `json:"rationale"`
	} `json:"acs"`
}
