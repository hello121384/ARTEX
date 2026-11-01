package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

var historyKinds = map[string]bool{
	db.KindBegin: true, db.KindGoal: true, db.KindIntent: true,
	db.KindFact: true, db.KindFinding: true, db.KindHint: true, db.KindDigest: true,
}

func compactHistoryNode(n *db.Node) map[string]any {
	var payload map[string]any
	_ = json.Unmarshal(n.Payload, &payload)
	out := map[string]any{"id": n.ID, "kind": n.Kind, "state": n.State}
	if n.Kind == db.KindDigest {
		out["summary"] = digestCardTitle(n)
	} else {
		for _, key := range []string{"summary", "text", "title", "body"} {
			if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
				out["summary"] = strings.TrimSpace(value)
				break
			}
		}
	}
	if summary, ok := out["summary"].(string); ok && len([]rune(summary)) > 240 {
		out["summary"] = string([]rune(summary)[:240]) + "…"
		out["summary_truncated"] = true
	}
	for _, key := range []string{"confidence", "conditions", "vulnclass", "severity"} {
		if value, ok := payload[key].(string); ok && value != "" {
			out[key] = value
		}
	}
	if n.BlockedReason != "" {
		out["blocked_reason"] = n.BlockedReason
	}
	if n.Inherited {
		inheritedMap(out, n.SourceTaskID)
	}
	return out
}

func (t *ToolSet) searchHistory() actool.CoreTool {
	return t.readExpTool("search_history",
		"按关键词、节点类型、状态或资产检索本任务及直接关联任务的完整历史，数据库侧过滤并分页，不受概览标题目录和最近节点窗口限制。默认查 intent/fact/digest；返回紧凑索引，Digest 用 expand_digest(id)，其他节点用 node_detail(id) 取完整证据。before 传上一页 next_before；total 始终是全部匹配数。",
		obj(map[string]any{
			"q":        str("关键词，匹配节点内容、来源或 #id；可选"),
			"kinds":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "节点类型：intent/fact/digest/finding/goal/hint/begin；默认 intent/fact/digest"},
			"states":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "按状态过滤，如 done/blocked/exhausted/confirmed/active；可选"},
			"asset_id": idp("资产 id；返回直接关联该资产的历史，以及覆盖相关成员的 Digest；可选"),
			"limit":    intp("返回条数，默认 20，上限 50"),
			"before":   intp("分页游标，传上一页 next_before；省略或 0 = 最新一页"),
		}),
		func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct {
				Q       string   `json:"q"`
				Kinds   []string `json:"kinds"`
				States  []string `json:"states"`
				AssetID int64    `json:"asset_id"`
				Limit   int      `json:"limit"`
				Before  int64    `json:"before"`
			}
			_ = json.Unmarshal(raw, &in)
			if len(in.Kinds) == 0 {
				in.Kinds = []string{db.KindIntent, db.KindFact, db.KindDigest}
			}
			seen := map[string]bool{}
			kinds := make([]string, 0, len(in.Kinds))
			for _, kind := range in.Kinds {
				kind = strings.ToLower(strings.TrimSpace(kind))
				if !historyKinds[kind] {
					return actool.Errorf("不支持的节点类型 " + kind), nil
				}
				if !seen[kind] {
					seen[kind] = true
					kinds = append(kinds, kind)
				}
			}
			if in.Limit <= 0 {
				in.Limit = 20
			}
			if in.Limit > 50 {
				in.Limit = 50
			}
			nodes, hasMore, total, err := t.ts.NodesBeforeWithSources(db.NodeFilter{
				Kinds: kinds, States: in.States, Query: strings.TrimSpace(in.Q), AssetID: in.AssetID,
			}, in.Before, in.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			items := make([]map[string]any, 0, len(nodes))
			for _, node := range nodes {
				items = append(items, compactHistoryNode(node))
			}
			out := map[string]any{"items": items, "total": total, "has_more": hasMore}
			if hasMore && len(nodes) > 0 {
				out["next_before"] = nodes[len(nodes)-1].ID
			}
			return jsonResult(out)
		})
}
