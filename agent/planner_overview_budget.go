package agent

import (
	"fmt"
	"strings"
)

const plannerOverviewRuneBudget = 18_000

func workerContextManyIDs(v any, threshold int) bool {
	switch ids := v.(type) {
	case []int64:
		return len(ids) > threshold
	case []any:
		return len(ids) > threshold
	default:
		return false
	}
}

// plannerOverviewBrief is used ONLY when the full view is too large. Current
// goals, hints, live work and findings go first; history is an index + bounded
// decision previews. Nothing is deleted from the graph or its retrieval tools.
func plannerOverviewBrief(data map[string]any) string {
	var b strings.Builder
	b.WriteString("【本轮关键态势（大图索引模式）】历史较多：以下仅显示优先级较高的记录。未出现≠未测试；完整历史使用 search_history，证据 node_detail(id)，摘要 expand_digest(id)。\n")
	if task, ok := data["task"].(map[string]any); ok {
		fmt.Fprintf(&b, "【任务】%s；目标=%s\n", ovText(task["description"], 400), ovText(task["goal"], 1200))
	}
	fmt.Fprintf(&b, "【统计】frontier_open=%s running_intents_total=%s done=%s facts=%s findings=%s hints=%d\n",
		ovText(data["frontier_open"], 32), ovText(data["running_intents_total"], 32),
		ovText(data["done_intents_total"], 32), ovText(data["facts"], 32), ovText(data["findings_total"], 32), len(ovMaps(data["hints"])))
	if cov, ok := data["coverage"].(map[string]any); ok {
		fmt.Fprintf(&b, "【覆盖】hosts=%s tested=%s/%s pct=%s；%s\n",
			ovText(cov["host_count"], 25), ovText(cov["tested"], 25), ovText(cov["denominator"], 25),
			ovPct(cov["pct"]), ovText(cov["status"], 80))
	}
	briefRows(&b, "目标", ovMaps(data["goals"]), "text", 16, 400)
	briefRows(&b, "人工提示（如有遗漏按原始图查）", ovMaps(data["hints"]), "text", 12, 600)
	briefRows(&b, "执行中", ovMaps(data["running_intents"]), "summary", 28, 220)
	briefRows(&b, "开放意图", ovMaps(data["open_intents"]), "summary", 22, 210)
	briefRows(&b, "近期漏洞", ovMaps(data["finding_list"]), "summary", 8, 250)
	briefRows(&b, "近期事实", ovMaps(data["recent_facts"]), "summary", 12, 200)
	briefRows(&b, "近期结束意图", ovMaps(data["recent_done_intents"]), "summary", 8, 170)

	titles := ovMaps(data["cold_digest_titles"])
	if len(titles) > 0 {
		fmt.Fprintf(&b, "【历史 Digest 标题目录】显示%d/%d组：\n", min(len(titles), 32), len(titles))
		for i, title := range titles {
			if i >= 32 {
				break
			}
			fmt.Fprintf(&b, "- #%s %s（成员=%s）\n",
				ovText(title["id"], 24), ovText(title["title"], 130), ovText(title["member_count"], 25))
		}
	}
	digests := ovMaps(data["cold_digests"])
	if len(digests) > 0 {
		fmt.Fprintf(&b, "【历史 Digest】共%d段；正文/成员未列全时通过 expand_digest(id) 恢复：\n", len(digests))
		for i, dg := range digests {
			if i >= 7 {
				fmt.Fprintf(&b, "  ...另有%d段，按 search_history(kinds=[digest]) 检索。\n", len(digests)-i)
				break
			}
			fmt.Fprintf(&b, "- Digest #%s %s；%s\n",
				ovText(dg["id"], 25), ovText(dg["title"], 150), ovText(dg["body"], 340))
			briefDecisionRows(&b, "Intent", ovMaps(dg["work"]), "intent_id", "goal", 3, 160)
			briefDecisionRows(&b, "Fact", ovMaps(dg["key_results"]), "fact_id", "summary", 4, 180)
			briefDecisionRows(&b, "未完成", ovMaps(dg["unresolved"]), "intent_id", "reason", 3, 160)
		}
	}
	briefRows(&b, "未压缩冷节点", ovMaps(data["uncompressed_cold_nodes"]), "summary", 6, 150)
	if n := ovText(data["uncompressed_cold_total"], 25); n != "" && n != "0" {
		fmt.Fprintf(&b, "未压缩冷节点总数=%s；更多按 search_history 检索。\n", n)
	}
	sources := ovMaps(data["related_tasks"])
	if len(sources) > 0 {
		fmt.Fprintf(&b, "【直接关联任务（只读）】共%d个，仅列关键入口：\n", len(sources))
		for i, src := range sources {
			if i >= 4 {
				fmt.Fprintf(&b, "  ...另有%d个来源任务；用 graph_overview/search_history 检索。\n", len(sources)-i)
				break
			}
			task, _ := src["task"].(map[string]any)
			fmt.Fprintf(&b, "- 来源 Task #%s %s；目标=%s\n",
				ovText(src["source_task_id"], 25), ovText(task["description"], 140), ovText(task["goal"], 180))
			briefRows(&b, "  来源提示", ovMaps(src["hints"]), "text", 2, 180)
			briefRows(&b, "  来源发现", ovMaps(src["recent_findings"]), "summary", 3, 160)
			briefRows(&b, "  来源事实", ovMaps(src["recent_facts"]), "summary", 3, 160)
			briefRows(&b, "  来源历史", ovMaps(src["recent_intent_results"]), "summary", 3, 150)
			briefDecisionRows(&b, "  来源 Digest", ovMaps(src["cold_digests"]), "id", "title", 3, 150)
		}
	}
	b.WriteString("【下一步】仅对具体候选方向调用 search_history(q/asset_id/states)，确认是否新机会；不要因摘要截断而自动重测。完整图可由 graph_overview 恢复。\n")
	// Defensive final bound for extraordinary user text/hints/IDs. Critical
	// live/task sections are first; an explicit marker signals missing tail.
	r := []rune(b.String())
	if len(r) > plannerOverviewRuneBudget {
		return string(r[:plannerOverviewRuneBudget-100]) +
			"\n【输出预算达到上限】剩余内容未注入，必须使用 search_history/graph_overview 按需回源。"
	}
	return strings.TrimSpace(b.String())
}

func briefRows(b *strings.Builder, title string, rows []map[string]any, field string, max, textLimit int) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(b, "【%s】显示%d/%d条\n", title, min(len(rows), max), len(rows))
	for i, row := range rows {
		if i >= max {
			break
		}
		fmt.Fprintf(b, "- #%s [%s] %s", ovText(row["id"], 24),
			ovText(row["state"], 24), ovText(row[field], textLimit))
		if v := ovText(row["confidence"], 25); v != "" {
			fmt.Fprintf(b, " confidence=%s", v)
		}
		if v := ovText(row["blocked_reason"], 120); v != "" {
			fmt.Fprintf(b, " blocked_reason=%s", v)
		}
		if id := ovText(row["source_task_id"], 22); id != "" {
			fmt.Fprintf(b, " source_task_id=%s", id)
		}
		b.WriteByte('\n')
	}
	if len(rows) > max {
		fmt.Fprintf(b, "  还有%d条未注入；按 ID/条件回源。\n", len(rows)-max)
	}
}

func briefDecisionRows(b *strings.Builder, title string, rows []map[string]any, idKey, textKey string, max, textLimit int) {
	for i, row := range rows {
		if i >= max {
			fmt.Fprintf(b, "  %s 还有%d条；展开 Digest 获取全部。\n", title, len(rows)-max)
			break
		}
		fmt.Fprintf(b, "  %s #%s [%s/%s] %s\n", title, ovText(row[idKey], 24),
			ovText(row["state"], 25), ovText(row["confidence"], 25), ovText(row[textKey], textLimit))
	}
}
