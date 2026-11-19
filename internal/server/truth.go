package server

import (
	"errors"
	"net/http"
	"strings"

	"verdict/internal/server/store"
)

// ---- 人工真值 ----

// 真值只用于验收测试；分析型 run 没有真值。
func (s *Server) truthTarget(w http.ResponseWriter, r *http.Request) (store.Run, bool) {
	run, err := s.st.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return store.Run{}, false
	}
	if run.Kind == store.KindPostmortem {
		fail(w, http.StatusBadRequest, "分析型任务没有人工真值")
		return store.Run{}, false
	}
	return run, true
}

func (s *Server) getTruth(w http.ResponseWriter, r *http.Request) {
	run, valid := s.truthTarget(w, r)
	if !valid {
		return
	}
	t, err := s.st.Truth(r.Context(), run.ID)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "还没有标记人工结论")
		return
	}
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, t)
}

func (s *Server) putTruth(w http.ResponseWriter, r *http.Request) {
	run, valid := s.truthTarget(w, r)
	if !valid {
		return
	}
	var in struct {
		Verdict   string `json:"verdict"`
		Source    string `json:"source"`
		Note      string `json:"note"`
		Ticket    string `json:"ticket"`
		TicketRef string `json:"ticket_ref"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	switch in.Verdict {
	case store.TruthPass, store.TruthFail, store.TruthInvalid:
	default:
		fail(w, http.StatusBadRequest, "人工结论只能是 pass / fail / invalid")
		return
	}
	source := strings.TrimSpace(in.Source)
	switch source {
	case "":
		source = "manual"
	case "manual", "ticket":
	default:
		fail(w, http.StatusBadRequest, "来源只能是 manual / ticket")
		return
	}
	// 人工判未通过时留个说明，否则以后谁也说不清当时为什么这么标。
	if in.Verdict != store.TruthPass && strings.TrimSpace(in.Note) == "" && strings.TrimSpace(in.Ticket) == "" {
		fail(w, http.StatusBadRequest, "判为未通过或无效时，请填写说明或贴上单子内容")
		return
	}
	t := store.Truth{RunID: run.ID, Verdict: in.Verdict, Source: source,
		Note: strings.TrimSpace(in.Note), Ticket: in.Ticket, TicketRef: strings.TrimSpace(in.TicketRef)}
	if err := s.st.SetTruth(r.Context(), t); err != nil {
		failErr(w, err)
		return
	}
	saved, err := s.st.Truth(r.Context(), run.ID)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, saved)
}

func (s *Server) deleteTruth(w http.ResponseWriter, r *http.Request) {
	run, valid := s.truthTarget(w, r)
	if !valid {
		return
	}
	if err := s.st.DeleteTruth(r.Context(), run.ID); err != nil {
		failErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}
