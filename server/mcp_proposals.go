package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func mcpInstructionsForToken(token *db.APIToken) string {
	if token.HasScope(db.APIScopeAssetsRead) || token.HasScope(db.APIScopeTaskPropose) {
		return fmt.Sprintf("ARTEX 企业资产与任务草稿。授权企业 ID 为 %v。任务草稿由操作人在任务页审核。%s", token.CompanyIDs, mcpUntrustedNote)
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
