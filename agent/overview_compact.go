package agent

// Read-only Planner presentation adapter. graph_overview retains the complete
// structured JSON; this view renders the same data without changing graph state.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PlannerOverviewPreview renders the current graph through the same converter
// used by Planner. It is read-only and also returns the original JSON for comparison.
func (t *ToolSet) PlannerOverviewPreview() (compact, full string, err error) {
	data := t.graphOverviewData()
	raw, err := json.Marshal(data)
	if err != nil {
		return "", "", err
	}
	return plannerOverviewText(data), string(raw), nil
}

type overviewWriter struct {
	strings.Builder
	records []int
}

func (b *overviewWriter) record() {
	pos := b.Len()
	if len(b.records) == 0 || b.records[len(b.records)-1] != pos {
		b.records = append(b.records, pos)
	}
}

func plannerOverviewText(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	var b overviewWriter
	b.record()
	if task, ok := data["task"].(map[string]any); ok {
		fmt.Fprintf(&b, "【Task】%s\n目标: %s\n", ovText(task["description"], 800), ovText(task["goal"], 1600))
	}
	b.record()
	fmt.Fprintf(&b, "【黑板统计】frontier_open=%s done_intents_total=%s facts=%s findings_total=%s\n",
		ovText(data["frontier_open"], 20), ovText(data["done_intents_total"], 20),
		ovText(data["facts"], 20), ovText(data["findings_total"], 20))
	if cov, ok := data["coverage"].(map[string]any); ok && len(cov) > 0 {
		b.record()
		fmt.Fprintf(&b, "【资产概况】hosts=%s tested=%s denominator=%s pct=%s status=%s\n",
			ovText(cov["host_count"], 20), ovText(cov["tested"], 20),
			ovText(cov["denominator"], 20), ovPct(cov["pct"]), ovText(cov["status"], 100))
		if types := ovText(cov["by_type"], 950); types != "" {
			fmt.Fprintf(&b, "资产类型覆盖: %s\n", types)
		}
		if note := ovText(cov["note"], 500); note != "" {
			fmt.Fprintf(&b, "覆盖统计说明: %s\n", note)
		}
	}

	// The current task's title directory is its only ID catalog. Keep related-task
	// IDs here because their detailed directories may be lost to global truncation.
	digestTotal := len(ovMaps(data["cold_digest_titles"]))
	var relatedDigestIDs []string
	for _, source := range ovMaps(data["related_tasks"]) {
		digestTotal += len(ovMaps(source["cold_digest_titles"]))
		for _, digest := range ovMaps(source["cold_digests"]) {
			if id := ovText(digest["id"], 24); id != "" {
				relatedDigestIDs = append(relatedDigestIDs, id)
			}
		}
		if ids := ovIDs(source["cold_digests_more"]); ids != "" {
			relatedDigestIDs = append(relatedDigestIDs, ids)
		}
	}
	if digestTotal > 0 {
		b.record()
		fmt.Fprintf(&b, "【历史恢复入口】共%d组；本任务完整 ID 与主题见下方历史标题目录，摘要与证据按需 expand_digest(id)。\n", digestTotal)
		if len(relatedDigestIDs) > 0 {
			fmt.Fprintf(&b, "关联任务 Digest ID: %s。\n", ovText(strings.Join(relatedDigestIDs, ","), 8000))
		}
	}
	ovDigestTitles(&b, ovMaps(data["cold_digest_titles"]))

	// Bound high-priority sections separately so a long goal or hint cannot hide live work.
	running := ovMaps(data["running_intents"])
	hints := ovMaps(data["hints"])
	b.record()
	runningTotal := ovText(data["running_intents_total"], 20)
	if runningTotal == "" {
		runningTotal = "未知"
	}
	fmt.Fprintf(&b, "【关键状态】running_intents_total=%s，已获取=%d；hints=%d；总数未知时不得假定无运行任务，详细信息如有截断请调用 graph_overview。\n", runningTotal, len(running), len(hints))
	ovPriorityRows(&b, "执行中 running_intents", running, "summary", 340, 300)
	ovPriorityRows(&b, "人工提示 hints", hints, "text", 600, 50)
	ovRows(&b, "目标 goals", ovMaps(data["goals"]), "text", 600, 100)
	ovRows(&b, "开放意图 open_intents", ovMaps(data["open_intents"]), "summary", 340, 30)
	ovRows(&b, "近期发现 finding_list", ovMaps(data["finding_list"]), "summary", 340, 10)
	ovRows(&b, "近期事实 recent_facts", ovMaps(data["recent_facts"]), "summary", 340, 20)
	ovDigests(&b, ovMaps(data["cold_digests"]), data["cold_digests_more"], 7000, 700)
	ovUncompressedCold(&b, data)
	ovRows(&b, "近期完成/中断 recent_done_intents", ovMaps(data["recent_done_intents"]), "summary", 340, 12)

	sources := ovMaps(data["related_tasks"])
	if len(sources) > 0 {
		b.record()
		fmt.Fprintf(&b, "【直接关联任务（只读）共%d个】\n", len(sources))
		for _, src := range sources {
			task, _ := src["task"].(map[string]any)
			b.record()
			fmt.Fprintf(&b, "- 来源 Task #%s [%s] %s；目标=%s；facts=%s findings=%s\n",
				ovText(src["source_task_id"], 24), ovText(task["status"], 40),
				ovText(task["description"], 360), ovText(task["goal"], 600),
				ovText(src["facts"], 20), ovText(src["findings"], 20))
			ovRows(&b, "来源目标", ovMaps(src["goals"]), "text", 200, 8)
			ovRows(&b, "来源提示", ovMaps(src["hints"]), "text", 200, 6)
			ovRows(&b, "来源最近漏洞", ovMaps(src["recent_findings"]), "summary", 260, 6)
			ovRows(&b, "来源最近事实", ovMaps(src["recent_facts"]), "summary", 260, 12)
			ovRows(&b, "来源已结束意图", ovMaps(src["recent_intent_results"]), "summary", 260, 8)
			if scopes := ovMaps(src["asset_scope"]); len(scopes) > 0 {
				fmt.Fprintf(&b, "  来源资产范围（显示%d/%s）:", len(scopes), ovText(src["asset_scope_count"], 20))
				for _, scope := range scopes {
					value := scope["value"]
					if value == nil {
						value = scope["company_id"]
					}
					fmt.Fprintf(&b, " [%s:%s]", ovText(scope["kind"], 24), ovText(value, 120))
				}
				b.WriteByte('\n')
			}
			if cov, ok := src["asset_coverage"].(map[string]any); ok {
				fmt.Fprintf(&b, "  来源资产覆盖: tested=%s denominator=%s pct=%s\n",
					ovText(cov["tested"], 20), ovText(cov["denominator"], 20), ovPct(cov["pct"]))
			}
			ovDigestTitles(&b, ovMaps(src["cold_digest_titles"]))
			ovDigests(&b, ovMaps(src["cold_digests"]), src["cold_digests_more"], 2400, 500)
			ovUncompressedCold(&b, src)
			if src["summary_truncated"] == true {
				b.WriteString("  来源快照有截断；详情可按节点 ID 调阅。\n")
			}
		}
	}
	b.record()
	b.WriteString("【原始记录】以上是 graph_overview 同源只读压缩视图，不是完整证据。完整结构化快照用 graph_overview，折叠历史用 expand_digest(id)，单节点证据用 node_detail(id)，事实用 list_facts；未出现的内容不等于未测试。\n")
	return limitPlannerOverview(strings.TrimSpace(b.String()), b.records)
}

func ovPriorityRows(b *overviewWriter, title string, rows []map[string]any, field string, textLimit, limit int) {
	if len(rows) == 0 {
		return
	}
	var section overviewWriter
	ovRows(&section, title, rows, field, textLimit, limit)
	b.record()
	b.WriteString(limitOverview(section.String(), section.records, 12000, "\n  ...本节已截断；调用 graph_overview 查看完整信息。\n"))
}

func ovRows(b *overviewWriter, title string, rows []map[string]any, field string, textLimit, limit int) {
	if len(rows) == 0 {
		return
	}
	b.record()
	fmt.Fprintf(b, "【%s】(%d条)\n", title, len(rows))
	for i, item := range rows {
		if i >= limit {
			fmt.Fprintf(b, "  ...另有%d条未直接展示，按原始工具查阅。\n", len(rows)-i)
			break
		}
		b.record()
		fmt.Fprintf(b, "- #%s [%s] %s",
			ovText(item["id"], 24), ovText(item["state"], 24), ovText(item[field], textLimit))
		if v := ovIDs(item["parents"]); v != "" {
			fmt.Fprintf(b, " parents=[%s]", v)
		}
		if v := ovIDs(item["yields"]); v != "" {
			fmt.Fprintf(b, " yields=[%s]", v)
		}
		if v := ovIDs(item["asset_ids"]); v != "" {
			fmt.Fprintf(b, " asset_ids=[%s]", v)
		}
		for _, attr := range []string{"kind", "confidence", "from_intent", "source_task_id", "result_summary", "blocked_reason"} {
			if v := ovText(item[attr], 300); v != "" {
				fmt.Fprintf(b, " %s=%s", attr, v)
			}
		}
		if refs := item["traffic_refs"]; refs != nil {
			if encoded, err := json.Marshal(refs); err == nil {
				fmt.Fprintf(b, " traffic_refs=%s", encoded)
			}
		}
		b.WriteByte('\n')
	}
}

func ovUncompressedCold(b *overviewWriter, data map[string]any) {
	total := ovText(data["uncompressed_cold_total"], 20)
	if total == "" || total == "0" {
		return
	}
	displayed := map[string]bool{}
	for _, key := range []string{"recent_facts", "recent_done_intents", "finding_list", "recent_findings", "recent_intent_results"} {
		for _, row := range ovMaps(data[key]) {
			displayed[ovText(row["id"], 24)] = true
		}
	}
	rows := ovMaps(data["uncompressed_cold_nodes"])
	filtered := make([]map[string]any, 0, len(rows))
	duplicates := map[string]bool{}
	for _, row := range rows {
		id := ovText(row["id"], 24)
		if displayed[id] {
			duplicates[id] = true
			continue
		}
		filtered = append(filtered, row)
	}
	var more []string
	switch ids := data["uncompressed_cold_more"].(type) {
	case []int64:
		for _, id := range ids {
			more = append(more, fmt.Sprint(id))
		}
	case []any:
		for _, id := range ids {
			more = append(more, fmt.Sprint(id))
		}
	}
	uniqueMore := more[:0]
	for _, id := range more {
		if displayed[id] {
			duplicates[id] = true
			continue
		}
		uniqueMore = append(uniqueMore, id)
	}
	b.record()
	fmt.Fprintf(b, "【未压缩冷节点】共%s条，另列%d条，%d条已在近期窗口展示；这些节点未被 Digest 覆盖。目录只用于感知和按需定位，不要逐项展开；精确查重用 search_history，关键原文用 node_detail(id)。\n", total, len(filtered)+len(uniqueMore), len(duplicates))
	ovRows(b, "未压缩冷节点目录", filtered, "summary", 340, len(filtered))
	if len(uniqueMore) > 0 {
		b.record()
		fmt.Fprintf(b, "  其他未压缩冷节点 ID: %s；可用 search_history 或 node_detail(id) 回源。\n", strings.Join(uniqueMore, ","))
	}
}

func ovDigestTitles(b *overviewWriter, titles []map[string]any) {
	if len(titles) == 0 {
		return
	}
	b.record()
	fmt.Fprintf(b, "【历史标题目录】共%d组；按标题初筛，精确查重用 search_history；摘要与证据按需 expand_digest(id)。\n", len(titles))
	for _, item := range titles {
		b.record()
		fmt.Fprintf(b, "- #%s %s（成员=%s）\n",
			ovText(item["id"], 24), ovText(item["title"], 80), ovText(item["member_count"], 20))
	}
}

func ovDigests(b *overviewWriter, digests []map[string]any, more any, bodyBudget, perBody int) {
	if len(digests) == 0 && ovIDs(more) == "" {
		return
	}
	b.record()
	fmt.Fprintf(b, "【精选历史摘要】显示%d段；完整证据按需 expand_digest(id)。\n", len(digests))
	for _, d := range digests {
		b.record()
		id := ovText(d["id"], 24)
		fmt.Fprintf(b, "- Digest #%s %s（成员=%s）\n", id, ovText(d["title"], 80), ovText(d["member_count"], 20))
		b.record()
		original, _ := d["body"].(string)
		runes := []rune(original)
		limit := perBody
		if bodyBudget < limit {
			limit = bodyBudget
		}
		if limit < 0 {
			limit = 0
		}
		if len(runes) > 0 && limit == 0 {
			fmt.Fprintf(b, "  原始摘要未展示（预算已用尽）；完整正文: expand_digest(%s)。\n", id)
			runes = nil
		} else {
			truncated := len(runes) > limit
			if truncated {
				runes = runes[:limit]
			}
			if len(runes) > 0 {
				fmt.Fprintf(b, "  原始摘要: %s\n", string(runes))
			}
			if truncated {
				fmt.Fprintf(b, "  摘要已截断；完整正文: expand_digest(%s)。\n", id)
			}
		}
		bodyBudget -= len(runes)
		ovDigestDecisions(b, d)
	}
	if ids := ovIDs(more); ids != "" {
		b.record()
		fmt.Fprintf(b, "  其余摘要未直接展示；完整 ID 见历史标题目录，按需 expand_digest(id) 找回。\n")
	}
}

func ovDigestDecisions(b *overviewWriter, digest map[string]any) {
	for _, item := range ovMaps(digest["work"]) {
		b.record()
		fmt.Fprintf(b, "  - Intent #%s [%s] %s\n",
			ovText(item["intent_id"], 24), ovText(item["state"], 20), ovText(item["goal"], 500))
	}
	for _, result := range ovMaps(digest["key_results"]) {
		b.record()
		fmt.Fprintf(b, "  - Fact #%s [%s/%s] %s",
			ovText(result["fact_id"], 24), ovText(result["state"], 20),
			ovText(result["confidence"], 20), ovText(result["summary"], 800))
		if conditions := ovText(result["conditions"], 600); conditions != "" {
			fmt.Fprintf(b, "；conditions=%s", conditions)
		}
		b.WriteByte('\n')
	}
	for _, item := range ovMaps(digest["unresolved"]) {
		b.record()
		fmt.Fprintf(b, "  未完成: #%s [%s] %s\n",
			ovText(item["intent_id"], 24), ovText(item["state"], 20), ovText(item["reason"], 600))
	}
	if ids := ovDigestRefIDs(digest["source_refs"], "findings"); ids != "" {
		fmt.Fprintf(b, "  关联发现: %s\n", ids)
	}
}

func ovDigestRefIDs(value any, kind string) string {
	switch refs := value.(type) {
	case map[string][]int64:
		return ovIDs(refs[kind])
	case map[string]any:
		return ovIDs(refs[kind])
	default:
		return ""
	}
}

const plannerOverviewMaxRunes = 60000

func limitPlannerOverview(s string, boundaries []int) string {
	const footer = "\n【全局视图截断】后续完整记录未展示；调用 graph_overview 获取完整结构化快照，使用 expand_digest(id) 与 node_detail(id) 找回；未出现不等于未测试。"
	return limitOverview(s, boundaries, plannerOverviewMaxRunes, footer)
}

func limitOverview(s string, boundaries []int, maxRunes int, footer string) string {
	if len([]rune(s)) <= maxRunes {
		return s
	}
	budget := maxRunes - len([]rune(footer))
	var b strings.Builder
	used := 0
	if len(boundaries) == 0 || boundaries[0] != 0 {
		boundaries = append([]int{0}, boundaries...)
	}
	for i, start := range boundaries {
		end := len(s)
		if i+1 < len(boundaries) {
			end = boundaries[i+1]
		}
		if start >= end || start >= len(s) {
			continue
		}
		if end > len(s) {
			end = len(s)
		}
		part := s[start:end]
		partRunes := len([]rune(part))
		if used+partRunes > budget {
			return strings.TrimRight(b.String(), "\r\n ") + footer
		}
		b.WriteString(part)
		used += partRunes
	}
	return strings.TrimSpace(b.String()) + footer
}

func ovMaps(v any) []map[string]any {
	if values, ok := v.([]map[string]any); ok {
		return values
	}
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func ovPct(v any) string {
	switch p := v.(type) {
	case *float64:
		if p == nil {
			return ""
		}
		return fmt.Sprintf("%.1f%%", *p*100)
	case float64:
		return fmt.Sprintf("%.1f%%", p*100)
	default:
		return ovText(v, 24)
	}
}

func ovText(v any, limit int) string {
	if v == nil || limit <= 0 {
		return ""
	}
	s := strings.Join(strings.Fields(fmt.Sprint(v)), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	cut := limit
	minimum := limit * 3 / 5
	for i := limit - 1; i >= minimum; i-- {
		if strings.ContainsRune("。！？!?；;", r[i]) || (r[i] == '.' && (i+1 == len(r) || r[i+1] == ' ')) {
			cut = i + 1
			break
		}
	}
	return string(r[:cut]) + "…(截断)"
}

func ovIDs(v any) string {
	var parts []string
	switch ids := v.(type) {
	case []int64:
		for _, id := range ids {
			parts = append(parts, fmt.Sprint(id))
		}
	case []any:
		for _, id := range ids {
			parts = append(parts, fmt.Sprint(id))
		}
	}
	return strings.Join(parts, ",")
}
