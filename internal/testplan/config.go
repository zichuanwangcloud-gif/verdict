package testplan

import "strings"

// 档位。
const (
	TierS      = "S"
	TierA      = "A"
	TierB      = "B"
	TierC      = "C"
	TierCustom = "custom"
)

// UI 走查范围。
const (
	UINone     = ""
	UITargeted = "targeted"
	UIFull     = "full"
)

// Config 是一次测试的流程开关。档位只是一组预设值。
type Config struct {
	Tier        string `json:"tier"`
	BriefPlan   bool   `json:"brief_plan"`   // 简版计划
	ApprovePlan bool   `json:"approve_plan"` // 计划生成后暂停，等待人工确认
	Service     bool   `json:"service"`      // 由 agent 托管启动被测服务
	StaticOnly  bool   `json:"static_only"`  // 只用命令行、单测、读代码
	SelfReview  bool   `json:"self_review"`  // 蓝队输出前自查
	Stability   bool   `json:"stability"`    // 关键检查重复执行
	UI          string `json:"ui"`           // "" / targeted / full
	// ShotSelfCheck 要求 UI 阶段截完图后读图自检；关闭时提示词明确禁止声称回看过图片。
	ShotSelfCheck bool `json:"shot_self_check"`
	// ShotReview 增加独立的截图质检阶段，用新会话只读图判断能否作为证据。
	ShotReview bool    `json:"shot_review"`
	RedRounds  int     `json:"red_rounds"`   // 红蓝循环最多轮数
	CrossModel bool    `json:"cross_model"`  // 红队优先用另一种 harness
	Explore    bool    `json:"explore"`      // 验收标准以外的主动探索
	Judge      bool    `json:"judge"`        // 最终裁决
	MaxMinutes int     `json:"max_minutes"`  // 阶段耗时之和的上限
	MaxCostUSD float64 `json:"max_cost_usd"` // 0 表示不限

	// 以下由后端在创建测试时解析，不由用户直接填写。
	Harness    string `json:"harness"`     // 主 harness（蓝队、裁决）
	RedHarness string `json:"red_harness"` // 红队实际使用的 harness
}

// Preset 返回档位的预设配置；未知档位按 B。
func Preset(tier string) Config {
	switch strings.ToUpper(tier) {
	case TierC:
		return Config{Tier: TierC, BriefPlan: true, StaticOnly: true, MaxMinutes: 20}
	case TierA:
		return Config{Tier: TierA, Service: true, UI: UITargeted, ShotSelfCheck: true, ShotReview: true,
			RedRounds: 1, CrossModel: true, Judge: true, MaxMinutes: 90}
	case TierS:
		return Config{Tier: TierS, Service: true, Stability: true, UI: UIFull, ShotSelfCheck: true, ShotReview: true,
			RedRounds: 3, CrossModel: true, Explore: true, Judge: true, MaxMinutes: 180}
	default:
		return Config{Tier: TierB, Service: true, SelfReview: true, MaxMinutes: 45}
	}
}

// Presets 供前端展示。
func Presets() []Config {
	return []Config{Preset(TierS), Preset(TierA), Preset(TierB), Preset(TierC)}
}

// Normalize 修正越界的取值。
func (c Config) Normalize() Config {
	switch c.Tier {
	case TierS, TierA, TierB, TierC, TierCustom:
	default:
		c.Tier = TierCustom
	}
	switch c.UI {
	case UINone, UITargeted, UIFull:
	default:
		c.UI = UITargeted
	}
	if c.StaticOnly {
		// 静态验证不启动服务，也不做浏览器测试。
		c.Service, c.UI = false, UINone
	}
	if c.UI == UINone {
		// 没有 UI 走查就没有截图，质检与自检都没有意义。
		c.ShotSelfCheck, c.ShotReview = false, false
	}
	c.RedRounds = max(0, min(c.RedRounds, 5))
	if c.RedRounds == 0 {
		c.CrossModel = false
	}
	if c.MaxMinutes <= 0 {
		c.MaxMinutes = Preset(c.Tier).MaxMinutes
	}
	c.MaxMinutes = max(5, min(c.MaxMinutes, 24*60))
	c.MaxCostUSD = max(0, c.MaxCostUSD)
	return c
}

// Equal 判断两份配置的开关是否相同（不比较解析出的 harness）。
func (c Config) sameSwitches(o Config) bool {
	c.Tier, o.Tier = "", ""
	c.Harness, o.Harness, c.RedHarness, o.RedHarness = "", "", "", ""
	return c == o
}

// MatchTier 返回与开关完全一致的档位名，都不一致时返回 custom。
func (c Config) MatchTier() string {
	for _, p := range Presets() {
		if c.sameSwitches(p) {
			return p.Tier
		}
	}
	return TierCustom
}
