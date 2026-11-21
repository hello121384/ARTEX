package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func mcpInstructionsForToken(token *db.APIToken) string {
	if token.HasScope(db.APIScopeAssetsRead) || token.HasScope(db.APIScopeTaskPropose) || token.HasScope(db.APIScopeTaskAutoDispatch) {
		instruction := "授权企业 ID 为 %v。"
		if token.HasScope(db.APIScopeTaskAutoDispatch) {
			return fmt.Sprintf("ARTEX 企业资产。%s 外部 Agent 可自动创建并启动授权企业内的资产测试任务，必须只使用清单中的资产 ID。平台会跳过同目标重合资产。%s", fmt.Sprintf(instruction, token.CompanyIDs), mcpUntrustedNote)
		}
		return fmt.Sprintf("ARTEX 企业资产与任务草稿。"+instruction+"任务草稿由操作人在任务页审核。%s", token.CompanyIDs, mcpUntrustedNote)
	}
	return mcpInstructions
}

func mcpProposalSchema(withRequestID bool) map[string]any {
	props := map[string]any{
		"company_id": intParam("授权企业 ID"),
		"goal":       strParam("本草稿的工作目标"),
		"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}, "minItems": 1},
	}
	required := []string{"company_id", "goal", "asset_ids"}
	if withRequestID {
		props["request_id"] = strParam("本次提交的唯一标识。重试同一提交时保持不变")
		required = append(required, "request_id")
	}
	return objSchema(props, required...)
}

type mcpAssetRow struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	Domain     string `json:"domain,omitempty"`
	RootDomain string `json:"root_domain,omitempty"`
	IP         string `json:"ip,omitempty"`
	Port       *int   `json:"port,omitempty"`
	AppName    string `json:"app_name,omitempty"`
	URL        string `json:"url,omitempty"`
	Method     string `json:"method,omitempty"`
	LastSeen   string `json:"last_seen,omitempty"`
}

func mcpInventoryAsset(a *db.Asset) mcpAssetRow {
	row := mcpAssetRow{
		ID: a.ID, Type: a.Type, Domain: a.Domain, RootDomain: a.RootDomain,
		IP: a.IP, Port: a.Port, AppName: a.AppName, Method: a.Method, LastSeen: a.LastSeen,
	}
	// Present the endpoint location without user information, query tokens or fragments.
	if u, err := url.Parse(a.URL); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
		row.URL = u.String()
	}
	return row
}

func (s *Server) mcpListCompanyAssets(_ context.Context, token *db.APIToken, raw json.RawMessage) (actool.Result, error) {
	var a struct {
		CompanyID int64  `json:"company_id"`
		Type      string `json:"type"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
	}
	a.Limit, a.Type = 25, "root_domain"
	if err := json.Unmarshal(raw, &a); err != nil {
		return actool.Errorf("资产清单参数无效"), nil
	}
	if !mcpCompanyAllowed(token, a.CompanyID) || a.CompanyID <= 0 {
		return actool.Errorf("企业不可用"), nil
	}
	if a.Offset < 0 || a.Limit < 1 || a.Limit > 100 {
		return actool.Errorf("offset 或 limit 无效，每页可取 1 至 100 条"), nil
	}
	switch a.Type {
	case "root_domain", "subdomain", "ip", "app", "service", "endpoint":
	default:
		return actool.Errorf("不支持的资产类型"), nil
	}
	assets, err := s.m.PG().Assets().QueryByCompany(a.CompanyID, a.Type, a.Limit+1, a.Offset)
	if err != nil {
		return actool.Errorf("资产清单读取失败"), nil
	}
	hasMore := len(assets) > a.Limit
	if hasMore {
		assets = assets[:a.Limit]
	}
	rows := make([]mcpAssetRow, 0, len(assets))
	for _, asset := range assets {
		rows = append(rows, mcpInventoryAsset(asset))
	}
	out := map[string]any{"company_id": a.CompanyID, "assets": rows, "has_more": hasMore}
	if hasMore {
		out["next_offset"] = a.Offset + len(rows)
	}
	return jsonResult(out)
}

type mcpProposalRequest struct {
	db.ProposalInput
	RequestID string `json:"request_id"`
}

func mcpProposalArgs(token *db.APIToken, raw json.RawMessage) (db.ProposalInput, string, error) {
	var a mcpProposalRequest
	if err := json.Unmarshal(raw, &a); err != nil {
		return db.ProposalInput{}, "", errors.New("草稿参数无效")
	}
	if a.CompanyID <= 0 || !mcpCompanyAllowed(token, a.CompanyID) {
		return db.ProposalInput{}, "", errors.New("企业不可用")
	}
	return a.ProposalInput, a.RequestID, nil
}

func mcpProposalFailure(err error) actool.Result {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return actool.Errorf("企业或资产不可用")
	case errors.Is(err, db.ErrProposalInvalid), errors.Is(err, db.ErrProposalConflict):
		return actool.Errorf(err.Error())
	default:
		return actool.Errorf("草稿操作失败")
	}
}

func (s *Server) mcpCheckTaskOverlap(ctx context.Context, token *db.APIToken, raw json.RawMessage) (actool.Result, error) {
	input, _, err := mcpProposalArgs(token, raw)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	overlap, err := db.CheckProposalOverlap(ctx, s.m.PG(), input, token.CompanyIDs, 0)
	if err != nil {
		return mcpProposalFailure(err), nil
	}
	return jsonResult(overlap)
}

func (s *Server) mcpSubmitTaskDraft(ctx context.Context, token *db.APIToken, raw json.RawMessage) (actool.Result, error) {
	input, requestID, err := mcpProposalArgs(token, raw)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	draft, err := s.m.PG().SubmitTaskProposal(ctx, token.ID, requestID, input, token.CompanyIDs)
	if err != nil {
		return mcpProposalFailure(err), nil
	}
	return jsonResult(map[string]any{"draft_id": draft.ID, "status": draft.Status})
}

func (s *Server) mcpAutoDispatchTask(ctx context.Context, token *db.APIToken, raw json.RawMessage) (actool.Result, error) {
	input, requestID, err := mcpProposalArgs(token, raw)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	pg := s.m.PG()
	s.mcpCreateMu.Lock()
	defer s.mcpCreateMu.Unlock()

	if existing, found, err := pg.GetTaskProposalByRequest(ctx, token.ID, requestID, input, token.CompanyIDs); err != nil {
		return mcpProposalFailure(err), nil
	} else if found {
		switch existing.Status {
		case "confirmed":
			return jsonResult(map[string]any{"status": "dispatched", "task_id": existing.TaskID, "asset_ids": existing.AssetIDs, "duplicate_asset_ids": existing.DuplicateAssetIDs, "duplicate_task_ids": existing.DuplicateTaskIDs, "duplicate_draft_ids": existing.DuplicateDraftIDs, "already_processed": true})
		case "deduplicated":
			return jsonResult(map[string]any{"status": "deduplicated", "duplicate_asset_ids": existing.DuplicateAssetIDs, "duplicate_task_ids": existing.DuplicateTaskIDs, "duplicate_draft_ids": existing.DuplicateDraftIDs, "already_processed": true})
		case "withdrawn":
			return mcpProposalFailure(db.ErrProposalConflict), nil
		}
	}

	if message, err := s.mcpCheckQuota(token); err != nil {
		return actool.Errorf("任务配额检查失败"), nil
	} else if message != "" {
		return actool.Errorf(message), nil
	}

	s.m.companyMu.Lock()
	result, err := pg.AutoDispatchTaskProposal(ctx, token.ID, requestID, input, token.CompanyIDs)
	var task *Task
	if err == nil && result.Task != nil {
		task = s.m.registerCreatedTask(result.Task)
	}
	s.m.companyMu.Unlock()
	if err != nil {
		return mcpProposalFailure(err), nil
	}

	response := map[string]any{
		"status": result.Status, "asset_ids": result.AssetIDs,
		"duplicate_asset_ids": result.DuplicateAssetIDs, "duplicate_task_ids": result.DuplicateTaskIDs,
		"duplicate_draft_ids": result.DuplicateDraftIDs, "already_processed": result.AlreadyProcessed,
	}
	if result.TaskID > 0 {
		response["task_id"] = result.TaskID
	}
	if task != nil {
		if err := pg.RecordAPITokenTask(token.ID, result.TaskID); err != nil {
			response["warning"] = "任务已进入运行队列，但令牌用量记录失败。"
			log.Printf("[mcp-server] automatic task %s quota record: %v", task.ID, err)
		}
		if _, err := s.admitTask(task, "bootstrap"); err != nil {
			response["warning"] = "任务已创建，请在任务页查看运行状态。"
			log.Printf("[mcp-server] automatic task %s admission: %v", task.ID, err)
		}
	}
	return jsonResult(response)
}

func (s *Server) mcpGetTaskGraph(ctx context.Context, token *db.APIToken, raw json.RawMessage) (actool.Result, error) {
	result, err := s.mcpDelegate(ctx, token, raw, s.toolGetTaskGraph())
	if err != nil || result.IsError || len(token.CompanyIDs) == 0 {
		return result, err
	}
	var graph map[string]json.RawMessage
	var sources []json.RawMessage
	if json.Unmarshal([]byte(result.Flatten()), &graph) != nil || graph == nil || json.Unmarshal(graph["related_tasks"], &sources) != nil {
		return actool.Errorf("关联任务摘要解析失败"), nil
	}
	visible := make([]json.RawMessage, 0, len(sources))
	for _, source := range sources {
		var ref struct {
			ID json.Number `json:"source_task_id"`
		}
		if json.Unmarshal(source, &ref) != nil {
			continue
		}
		if _, ok := s.mcpLookupTask(token, ref.ID.String()); ok {
			visible = append(visible, source)
		}
	}
	graph["related_tasks"], err = json.Marshal(visible)
	if err != nil {
		return actool.Errorf("关联任务摘要解析失败"), nil
	}
	return jsonResult(graph)
}
