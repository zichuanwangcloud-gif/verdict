// Package api 暴露平台的 HTTP 接口。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"verdict/internal/agent"
	"verdict/internal/domain"
	"verdict/internal/ledger"
	"verdict/internal/store"
	"verdict/internal/verifier"
)

type Server struct {
	Store     *store.Store
	Agent     agent.Agent
	Verifiers *verifier.Registry
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) { ok(w, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/verifiers", func(w http.ResponseWriter, _ *http.Request) { ok(w, s.Verifiers.Catalog()) })

	mux.HandleFunc("GET /api/areas", s.listAreas)
	mux.HandleFunc("POST /api/areas", s.upsertArea)

	mux.HandleFunc("GET /api/requirements", s.listRequirements)
	mux.HandleFunc("POST /api/requirements", s.createRequirement)
	mux.HandleFunc("POST /api/requirements/{id}/draft-acs", s.draftACs)
	mux.HandleFunc("PUT /api/requirements/{id}/acs", s.replaceACs)
	mux.HandleFunc("PUT /api/requirements/{id}/disposition", s.setDisposition)

	mux.HandleFunc("GET /api/changesets", s.listChangeSets)
	mux.HandleFunc("POST /api/changesets", s.createChangeSet)
	mux.HandleFunc("GET /api/changesets/{id}/verdict", s.verdict)
	mux.HandleFunc("POST /api/changesets/{id}/run", s.run)
	return mux
}

// ---- helpers ----

func ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "请求体不是合法的 JSON: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, http.StatusBadRequest, "编号无效")
		return 0, false
	}
	return id, true
}

func internal(w http.ResponseWriter, err error) { fail(w, http.StatusInternalServerError, err.Error()) }

// ---- areas ----

func (s *Server) listAreas(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.Areas(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, out)
}

func (s *Server) upsertArea(w http.ResponseWriter, r *http.Request) {
	var a domain.Area
	if !decode(w, r, &a) {
		return
	}
	if a.Name = strings.TrimSpace(a.Name); a.Name == "" {
		fail(w, http.StatusBadRequest, "区域名不能为空")
		return
	}
	if err := s.Store.UpsertArea(r.Context(), a); err != nil {
		internal(w, err)
		return
	}
	ok(w, a)
}

// ---- requirements ----

func (s *Server) listRequirements(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.Requirements(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, out)
}

func (s *Server) createRequirement(w http.ResponseWriter, r *http.Request) {
	var in domain.Requirement
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Title) == "" {
		fail(w, http.StatusBadRequest, "标题不能为空")
		return
	}
	id, err := s.Store.CreateRequirement(r.Context(), in)
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, map[string]int64{"id": id})
}

// draftACs 让智能体起草验收条件，只返回草稿，不落库——由人确认后再保存。
func (s *Server) draftACs(w http.ResponseWriter, r *http.Request) {
	id, good := pathID(w, r)
	if !good {
		return
	}
	req, found, err := s.Store.Requirement(r.Context(), id)
	if err != nil {
		internal(w, err)
		return
	}
	if !found {
		fail(w, http.StatusNotFound, "需求不存在")
		return
	}
	areas, err := s.Store.Areas(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	drafts, err := s.Agent.DraftACs(r.Context(), req, areas, s.Verifiers.Catalog())
	if err != nil {
		fail(w, http.StatusBadGateway, "智能体调用失败: "+err.Error())
		return
	}
	ok(w, drafts)
}

func (s *Server) replaceACs(w http.ResponseWriter, r *http.Request) {
	id, good := pathID(w, r)
	if !good {
		return
	}
	var acs []domain.AC
	if !decode(w, r, &acs) {
		return
	}
	for i, a := range acs {
		if strings.TrimSpace(a.Text) == "" {
			fail(w, http.StatusBadRequest, "第 "+strconv.Itoa(i+1)+" 条验收条件为空")
			return
		}
		if a.Verifier != "" {
			if _, exists := s.Verifiers.Get(a.Verifier); !exists {
				fail(w, http.StatusBadRequest, "未知的验证器: "+a.Verifier)
				return
			}
		}
	}
	if err := s.Store.ReplaceACs(r.Context(), id, acs); err != nil {
		internal(w, err)
		return
	}
	ok(w, map[string]int{"count": len(acs)})
}

func (s *Server) setDisposition(w http.ResponseWriter, r *http.Request) {
	id, good := pathID(w, r)
	if !good {
		return
	}
	var in struct {
		Disposition domain.Disposition `json:"disposition"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch in.Disposition {
	case domain.DispositionActive, domain.DispositionClosed, domain.DispositionRemoved:
	default:
		fail(w, http.StatusBadRequest, "处置只能是 active / closed / removed")
		return
	}
	if err := s.Store.SetDisposition(r.Context(), id, in.Disposition); err != nil {
		internal(w, err)
		return
	}
	ok(w, in)
}

// ---- changesets ----

func (s *Server) listChangeSets(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.ChangeSets(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, out)
}

// createChangeSet 登记一个提测单位。给了 diff 且没给 touched_areas 时，
// 由智能体判断触碰的区域；它的原始分析随变更集存档。
func (s *Server) createChangeSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref          string   `json:"ref"`
		Digest       string   `json:"digest"`
		Diff         string   `json:"diff"`
		TouchedAreas []string `json:"touched_areas"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Ref = strings.TrimSpace(in.Ref); in.Ref == "" {
		fail(w, http.StatusBadRequest, "版本名不能为空")
		return
	}
	cs := domain.ChangeSet{Ref: in.Ref, Digest: in.Digest, TouchedAreas: in.TouchedAreas}
	if in.TouchedAreas == nil {
		if strings.TrimSpace(in.Diff) == "" {
			fail(w, http.StatusBadRequest, "需要提供 diff 或 touched_areas 之一")
			return
		}
		areas, err := s.Store.Areas(r.Context())
		if err != nil {
			internal(w, err)
			return
		}
		an, err := s.Agent.AnalyzeChange(r.Context(), in.Diff, areas)
		if err != nil {
			fail(w, http.StatusBadGateway, "智能体调用失败: "+err.Error())
			return
		}
		for _, a := range an.NewAreas {
			if err := s.Store.UpsertArea(r.Context(), a); err != nil {
				internal(w, err)
				return
			}
		}
		cs.TouchedAreas = an.TouchedAreas
		cs.Analysis, _ = json.Marshal(an)
	}
	id, err := s.Store.CreateChangeSet(r.Context(), cs)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			fail(w, http.StatusConflict, "版本名已存在: "+in.Ref)
			return
		}
		internal(w, err)
		return
	}
	ok(w, map[string]any{"id": id, "touched_areas": cs.TouchedAreas})
}

var errNotFound = errors.New("变更集不存在")

func (s *Server) compute(ctx context.Context, id int64) (domain.Verdict, error) {
	all, err := s.Store.ChangeSets(ctx)
	if err != nil {
		return domain.Verdict{}, err
	}
	var target *domain.ChangeSet
	for i := range all {
		if all[i].ID == id {
			target = &all[i]
		}
	}
	if target == nil {
		return domain.Verdict{}, errNotFound
	}
	reqs, err := s.Store.Requirements(ctx)
	if err != nil {
		return domain.Verdict{}, err
	}
	ev, err := s.Store.Evidence(ctx)
	if err != nil {
		return domain.Verdict{}, err
	}
	return ledger.Compute(*target, all, reqs, ev), nil
}

func (s *Server) verdict(w http.ResponseWriter, r *http.Request) {
	id, good := pathID(w, r)
	if !good {
		return
	}
	v, err := s.compute(r.Context(), id)
	if errors.Is(err, errNotFound) {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, v)
}

// run 在该变更集上执行所有「未测 / 已失效 / 未通过」的验收条件，然后返回新的裁决。
// 骨架阶段同步串行执行；后续应改为异步任务 + 隔离环境。
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	id, good := pathID(w, r)
	if !good {
		return
	}
	v, err := s.compute(r.Context(), id)
	if errors.Is(err, errNotFound) {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	ran := 0
	for _, ac := range ledger.NeedsRun(v) {
		vf, exists := s.Verifiers.Get(ac.Verifier)
		if !exists {
			continue
		}
		res := vf.Run(r.Context(), ac.Params)
		if err := s.Store.AddEvidence(r.Context(), domain.Evidence{
			ACID: ac.ID, ChangeSetID: id, Verifier: ac.Verifier, Passed: res.Passed, Output: res.Output,
		}); err != nil {
			internal(w, err)
			return
		}
		ran++
	}
	v, err = s.compute(r.Context(), id)
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, map[string]any{"ran": ran, "verdict": v})
}
