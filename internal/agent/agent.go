// Package agent 是平台里唯一接触被测项目具体内容的一层。
//
// 模型在这里做两件事：把需求拆成验收条件、把变更映射到 Area。
// 输出全部是结构化建议，由平台落库留痕；通过与否永远不由这里决定。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"verdict/internal/domain"
)

type ACDraft struct {
	Text     string          `json:"text"`
	Areas    []string        `json:"areas"`
	Verifier string          `json:"verifier"` // 空 = 无法自动验证
	Params   json.RawMessage `json:"params"`
	Reason   string          `json:"reason"`
}

type ChangeAnalysis struct {
	TouchedAreas []string      `json:"touched_areas"`
	NewAreas     []domain.Area `json:"new_areas"`
	Risk         string        `json:"risk"` // low / medium / high
	Reasoning    string        `json:"reasoning"`
}

type Agent interface {
	DraftACs(ctx context.Context, req domain.Requirement, areas []domain.Area, verifiers []map[string]string) ([]ACDraft, error)
	AnalyzeChange(ctx context.Context, diff string, areas []domain.Area) (ChangeAnalysis, error)
}

type Claude struct {
	client anthropic.Client
	model  string
	// fallback 开启时，被安全分类器拒绝的请求由服务端改用备用模型重跑。
	fallback bool
}

// NewClaude 从环境解析凭据（ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN / 登录档案）。
// VERDICT_MODEL 覆盖模型；经自建网关转发且网关不认 fallbacks 字段时设 VERDICT_NO_FALLBACK=1。
func NewClaude() *Claude {
	model := os.Getenv("VERDICT_MODEL")
	if model == "" {
		model = "claude-opus-5-5"
	}
	return &Claude{
		client:   anthropic.NewClient(),
		model:    model,
		fallback: os.Getenv("VERDICT_NO_FALLBACK") == "",
	}
}

const system = `你是发布验证平台的分析智能体。平台只认识「区域(Area)」这一个抽象：
它是变更与验收条件之间唯一的连接点，用来判断一次变更会推翻哪些已有的验证结论。

你的输出是建议，会被原样存档并由平台的确定性逻辑使用，所以：
- 区域名优先复用已有的；确实没有合适的才提新的，名字用小写英文加点号分层。
- 拿不准一处变更是否影响某个区域时，算作影响。漏判会让失效的结论被沿用，代价远高于多测一次。
- 不要判断测试是否通过，那不是你的职责。`

func (c *Claude) DraftACs(ctx context.Context, req domain.Requirement, areas []domain.Area, verifiers []map[string]string) ([]ACDraft, error) {
	prompt := fmt.Sprintf(`把下面的需求拆成逐条可独立验证的验收条件。

每条验收条件要绑定一个验证器并给出它的参数；如果现有验证器都无法验证这一条，
verifier 留空字符串，并在 reason 里说明缺什么（需求方会据此补充可测量的标准）。
params_json 是验证器参数的 JSON 字符串，不可验证时填 "{}"。

<requirement>
标题：%s
%s
</requirement>

<areas>
%s
</areas>

<verifiers>
%s
</verifiers>`, req.Title, req.Body, mustJSON(areas), mustJSON(verifiers))

	var out struct {
		ACs []struct {
			Text       string   `json:"text"`
			Areas      []string `json:"areas"`
			Verifier   string   `json:"verifier"`
			ParamsJSON string   `json:"params_json"`
			Reason     string   `json:"reason"`
		} `json:"acs"`
	}
	schema := object(map[string]any{
		"acs": array(object(map[string]any{
			"text":        str(),
			"areas":       array(str()),
			"verifier":    str(),
			"params_json": str(),
			"reason":      str(),
		})),
	})
	if err := c.ask(ctx, prompt, schema, &out); err != nil {
		return nil, err
	}
	drafts := make([]ACDraft, 0, len(out.ACs))
	for _, a := range out.ACs {
		params := json.RawMessage(a.ParamsJSON)
		if !json.Valid(params) {
			params = json.RawMessage(`{}`)
		}
		drafts = append(drafts, ACDraft{Text: a.Text, Areas: a.Areas, Verifier: a.Verifier, Params: params, Reason: a.Reason})
	}
	return drafts, nil
}

func (c *Claude) AnalyzeChange(ctx context.Context, diff string, areas []domain.Area) (ChangeAnalysis, error) {
	prompt := fmt.Sprintf(`判断下面这次变更触碰了哪些区域。

touched_areas 里既包含已有区域，也包含你在 new_areas 里新提出的区域名。
risk 取 low / medium / high，依据是这次变更出错时的代价与波及范围。

<areas>
%s
</areas>

<diff>
%s
</diff>`, mustJSON(areas), diff)

	var out ChangeAnalysis
	schema := object(map[string]any{
		"touched_areas": array(str()),
		"new_areas": array(object(map[string]any{
			"name":        str(),
			"description": str(),
		})),
		"risk":      map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
		"reasoning": str(),
	})
	err := c.ask(ctx, prompt, schema, &out)
	return out, err
}

func (c *Claude) ask(ctx context.Context, prompt string, schema map[string]any, out any) error {
	params := anthropic.BetaMessageNewParams{
		Model:     c.model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt)),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortHigh,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: schema},
		},
	}
	if c.fallback {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	resp, err := c.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return fmt.Errorf("模型接口返回 %d: %w", apiErr.StatusCode, err)
		}
		return err
	}
	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return errors.New("模型拒绝了这次请求")
	case anthropic.BetaStopReasonMaxTokens:
		return errors.New("模型输出被截断，未得到完整结果")
	}
	var text strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		return fmt.Errorf("模型输出不是预期的结构: %w", err)
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func str() map[string]any { return map[string]any{"type": "string"} }

func array(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func object(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}
