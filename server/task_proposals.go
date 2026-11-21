package server

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/db"
)

// These routes belong to the operator UI and require its normal JWT. MCP tokens
// can submit proposals; no MCP tool can invoke the review or confirmation routes.
func (s *Server) registerProposalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/task-proposals", s.listTaskProposals)
	mux.HandleFunc("GET /api/task-proposals/{id}", s.reviewTaskProposal)
	mux.HandleFunc("PATCH /api/task-proposals/{id}", s.editTaskProposal)
	mux.HandleFunc("POST /api/task-proposals/{id}/withdraw", s.withdrawTaskProposal)
	mux.HandleFunc("POST /api/task-proposals/{id}/confirm", s.confirmTaskProposal)
}

func writeProposalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrProposalConflict):
		writeProductError(w, http.StatusConflict, "proposal_conflict", "草稿或查重结果已变化，请刷新后重新核对。", nil)
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, http.StatusNotFound, "草稿或所选企业资产已不可用")
	case errors.Is(err, db.ErrProposalInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("[proposal] %v", err)
		writeErr(w, http.StatusInternalServerError, "无法读取或保存任务草稿")
	}
}

func (s *Server) listTaskProposals(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	q := r.URL.Query()
	offset, limit := 0, 50
	var err error
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
	}
	if err != nil || offset < 0 {
		writeErr(w, 400, "offset 无效")
		return
	}
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
	}
	if err != nil || limit < 1 || limit > 100 {
		writeErr(w, 400, "limit 应为 1 到 100")
		return
	}
	items, err := pg.ListTaskProposals(r.Context(), limit+1, offset)
	if err != nil {
		writeProposalError(w, err)
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"proposals": items, "has_more": more})
}

func (s *Server) loadReviewProposal(w http.ResponseWriter, r *http.Request) (*db.DB, *db.TaskProposal) {
	id, ok := pathInt(r, "id")
	if !ok || id < 1 {
		writeErr(w, 400, "草稿编号无效")
		return nil, nil
	}
	pg := s.pg(w)
	if pg == nil {
		return nil, nil
	}
	p, err := pg.GetTaskProposal(r.Context(), id)
	if err != nil {
		writeProposalError(w, err)
		return nil, nil
	}
	return pg, p
}

func (s *Server) reviewTaskProposal(w http.ResponseWriter, r *http.Request) {
	pg, p := s.loadReviewProposal(w, r)
	if p == nil {
		return
	}
	data := map[string]any{"proposal": p, "overlap": nil, "review_key": ""}
	if p.Status == "pending" && len(p.Assets) == len(p.AssetIDs) {
		// The authenticated operator can review other tasks involving this
		// company, including tasks associated with multiple companies.
		overlap, err := db.CheckProposalOverlap(r.Context(), pg, p.ProposalInput, nil, p.ID)
		if err != nil {
			writeProposalError(w, err)
			return
		}
		data["overlap"], data["review_key"] = overlap, overlap.ReviewKey
	}
	writeJSON(w, 200, data)
}

type proposalReviewRequest struct {
	Version   int64   `json:"version"`
	Goal      string  `json:"goal"`
	AssetIDs  []int64 `json:"asset_ids"`
	ReviewKey string  `json:"review_key"`
}

func readProposalReview(w http.ResponseWriter, r *http.Request) (proposalReviewRequest, bool) {
	var req proposalReviewRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := decode(r, &req); err != nil || req.Version < 1 {
		writeErr(w, 400, "草稿版本或参数无效")
		return req, false
	}
	return req, true
}

func (s *Server) editTaskProposal(w http.ResponseWriter, r *http.Request) {
	pg, p := s.loadReviewProposal(w, r)
	if p == nil {
		return
	}
	req, ok := readProposalReview(w, r)
	if !ok {
		return
	}
	p, err := pg.EditTaskProposal(r.Context(), p.ID, req.Version, db.ProposalInput{CompanyID: p.CompanyID, AssetIDs: req.AssetIDs, Goal: req.Goal})
	if err != nil {
		writeProposalError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"proposal": p})
}

func (s *Server) withdrawTaskProposal(w http.ResponseWriter, r *http.Request) {
	pg, p := s.loadReviewProposal(w, r)
	if p == nil {
		return
	}
	req, ok := readProposalReview(w, r)
	if !ok {
		return
	}
	p, err := pg.WithdrawTaskProposal(r.Context(), p.ID, req.Version)
	if err != nil {
		writeProposalError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"proposal": p})
}

func (s *Server) confirmTaskProposal(w http.ResponseWriter, r *http.Request) {
	pg, p := s.loadReviewProposal(w, r)
	if p == nil {
		return
	}
	req, ok := readProposalReview(w, r)
	if !ok {
		return
	}
	if req.ReviewKey == "" {
		writeErr(w, 400, "请先查看当前查重结果")
		return
	}
	// Publish a committed task to the live manager before a simultaneous
	// confirmation retry or company edit can observe its ID.
	s.m.companyMu.Lock()
	pt, id, err := pg.ConfirmTaskProposal(r.Context(), p.ID, req.Version, req.ReviewKey)
	var task *Task
	if err == nil && pt != nil {
		task = s.m.registerCreatedTask(pt)
	}
	s.m.companyMu.Unlock()
	if err != nil {
		writeProposalError(w, err)
		return
	}
	result := map[string]string{"task_id": strconv.FormatInt(id, 10)}
	if task != nil {
		// The approved asset links already exist. Re-parsing the goal to seed
		// another asset would extend the selection made by the reviewer.
		if _, err := s.admitTask(task, "bootstrap"); err != nil {
			log.Printf("[proposal] admission for task %s: %v", task.ID, err)
			result["warning"] = "任务已创建，请在任务页查看排队状态。"
		}
	}
	writeJSON(w, 200, result)
}
