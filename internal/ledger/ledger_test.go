package ledger

import (
	"testing"

	"verdict/internal/domain"
)

func TestCarryForwardAndInvalidate(t *testing.T) {
	rc1 := domain.ChangeSet{ID: 1, Seq: 1, Ref: "rc.1", TouchedAreas: []string{"billing", "auth"}}
	rc2 := domain.ChangeSet{ID: 2, Seq: 2, Ref: "rc.2", TouchedAreas: []string{"billing"}}
	all := []domain.ChangeSet{rc1, rc2}
	reqs := []domain.Requirement{{
		ID: 1, Disposition: domain.DispositionActive,
		ACs: []domain.AC{
			{ID: 10, Verifier: "command", Areas: []string{"auth"}},
			{ID: 11, Verifier: "command", Areas: []string{"billing"}},
			{ID: 12, Verifier: "", Areas: []string{"auth"}},
			{ID: 13, Verifier: "command"}, // 无 Area：任何变更都失效
		},
	}}
	ev := []domain.Evidence{
		{ID: 1, ACID: 10, ChangeSetID: 1, Passed: true},
		{ID: 2, ACID: 11, ChangeSetID: 1, Passed: true},
		{ID: 3, ACID: 13, ChangeSetID: 1, Passed: true},
	}

	v := Compute(rc2, all, reqs, ev)
	got := map[int64]domain.ACVerdict{}
	for _, a := range v.Requirements[0].ACs {
		got[a.AC.ID] = a
	}
	if got[10].Status != domain.StatusVerified || got[10].CarriedFrom != "rc.1" {
		t.Errorf("AC10 应沿用 rc.1 的结论, got %+v", got[10])
	}
	if got[11].Status != domain.StatusStale || got[11].InvalidatedBy != "rc.2" {
		t.Errorf("AC11 应被 rc.2 推翻, got %+v", got[11])
	}
	if got[12].Status != domain.StatusUnverifiable {
		t.Errorf("AC12 应为不可验证, got %+v", got[12])
	}
	if got[13].Status != domain.StatusStale {
		t.Errorf("AC13 无 Area 应失效, got %+v", got[13])
	}
	if v.Releasable {
		t.Error("存在失效与不可验证项，不应可发")
	}
	if n := len(NeedsRun(v)); n != 2 {
		t.Errorf("应有 2 条待执行, got %d", n)
	}

	// 在 rc.1 上看：三条都有效。
	v1 := Compute(rc1, all, reqs, ev)
	if v1.Counts[domain.StatusVerified] != 3 {
		t.Errorf("rc.1 上应有 3 条通过, got %v", v1.Counts)
	}
}

func TestReleasable(t *testing.T) {
	rc := domain.ChangeSet{ID: 1, Seq: 1, Ref: "rc.1"}
	reqs := []domain.Requirement{
		{ID: 1, Disposition: domain.DispositionActive, ACs: []domain.AC{{ID: 1, Verifier: "command", Areas: []string{"a"}}}},
		{ID: 2, Disposition: domain.DispositionRemoved, ACs: []domain.AC{{ID: 2, Verifier: "command", Areas: []string{"a"}}}},
	}
	fail := []domain.Evidence{{ID: 1, ACID: 1, ChangeSetID: 1, Passed: false}}
	if Compute(rc, []domain.ChangeSet{rc}, reqs, fail).Releasable {
		t.Error("失败证据不应可发")
	}
	pass := append(fail, domain.Evidence{ID: 2, ACID: 1, ChangeSetID: 1, Passed: true})
	if !Compute(rc, []domain.ChangeSet{rc}, reqs, pass).Releasable {
		t.Error("同一变更集上较新的通过证据应生效，且已摘除的需求不阻断")
	}
}
