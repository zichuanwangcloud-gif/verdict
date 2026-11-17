// Package ledger 是台账的判定核心：纯函数、确定性、不调用模型。
package ledger

import (
	"sort"

	"verdict/internal/domain"
)

// Compute 计算目标变更集上的裁决。
//
// 有效性规则：AC 在变更集 M 上的证据，到目标变更集 N 仍然有效，
// 当且仅当 (M, N] 之间没有任何变更集触碰该 AC 涉及的 Area。
// AC 未声明 Area 时无法证明不受影响，任何后续变更都使其失效。
func Compute(target domain.ChangeSet, all []domain.ChangeSet, reqs []domain.Requirement, evidence []domain.Evidence) domain.Verdict {
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	bySeq := map[int64]domain.ChangeSet{}
	byID := map[int64]domain.ChangeSet{}
	for _, c := range all {
		bySeq[c.Seq] = c
		byID[c.ID] = c
	}

	// 每条 AC 取目标变更集及之前最新的一条证据。
	latest := map[int64]domain.Evidence{}
	for _, e := range evidence {
		cs, ok := byID[e.ChangeSetID]
		if !ok || cs.Seq > target.Seq {
			continue
		}
		cur, has := latest[e.ACID]
		if !has || byID[cur.ChangeSetID].Seq < cs.Seq ||
			(byID[cur.ChangeSetID].Seq == cs.Seq && e.ID > cur.ID) {
			latest[e.ACID] = e
		}
	}

	v := domain.Verdict{ChangeSet: target, Releasable: true, Counts: map[domain.ACStatus]int{}}
	for _, r := range reqs {
		rv := domain.RequirementVerdict{Requirement: r}
		for _, ac := range r.ACs {
			av := judge(ac, target, all, byID, latest)
			rv.ACs = append(rv.ACs, av)
			v.Counts[av.Status]++
			if av.Status != domain.StatusVerified {
				rv.Blocking = true
			}
		}
		if len(r.ACs) == 0 {
			rv.Blocking = true // 没有验收条件的需求无从放行
		}
		// 骨架阶段：closed / removed 直接豁免。
		// TODO: closed 应要求「关闭状态下行为与上一变更集一致」的证据。
		if r.Disposition != domain.DispositionActive {
			rv.Blocking = false
		}
		if rv.Blocking {
			v.Releasable = false
		}
		v.Requirements = append(v.Requirements, rv)
	}
	if len(reqs) == 0 {
		v.Releasable = false
	}
	return v
}

func judge(ac domain.AC, target domain.ChangeSet, all []domain.ChangeSet, byID map[int64]domain.ChangeSet, latest map[int64]domain.Evidence) domain.ACVerdict {
	av := domain.ACVerdict{AC: ac}
	if ac.Verifier == "" {
		av.Status = domain.StatusUnverifiable
		return av
	}
	e, ok := latest[ac.ID]
	if !ok {
		av.Status = domain.StatusUntested
		return av
	}
	av.Evidence = &e
	from := byID[e.ChangeSetID]
	for _, c := range all {
		if c.Seq <= from.Seq || c.Seq > target.Seq {
			continue
		}
		if len(ac.Areas) == 0 || intersects(ac.Areas, c.TouchedAreas) {
			av.Status = domain.StatusStale
			av.InvalidatedBy = c.Ref
			return av
		}
	}
	if from.ID != target.ID {
		av.CarriedFrom = from.Ref
	}
	if e.Passed {
		av.Status = domain.StatusVerified
	} else {
		av.Status = domain.StatusFailed
	}
	return av
}

func intersects(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := set[y]; ok {
			return true
		}
	}
	return false
}

// NeedsRun 返回目标变更集上需要执行验证器的 AC。
func NeedsRun(v domain.Verdict) []domain.AC {
	var out []domain.AC
	for _, r := range v.Requirements {
		if r.Requirement.Disposition != domain.DispositionActive {
			continue
		}
		for _, a := range r.ACs {
			switch a.Status {
			case domain.StatusUntested, domain.StatusStale, domain.StatusFailed:
				out = append(out, a.AC)
			}
		}
	}
	return out
}
