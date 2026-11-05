package agent

import (
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestSearchHistoryFiltersAndPages(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("history search tool", "history search tool", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "verify tenant authorization marker"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetIntentState(intent, "blocked"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetIntentBlockedReason(intent, "second tenant unavailable"); err != nil {
		t.Fatal(err)
	}
	fact, err := store.AddNode(db.KindFact, map[string]any{"summary": "tenant A readable by tenant B", "confidence": "observed"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.AddDigest(map[string]any{"title": "tenant authorization", "body": "authorization history body"}, []int64{intent, fact})
	if err != nil {
		t.Fatal(err)
	}

	tools := NewToolSet(store, "planner")
	blocked := callReadJSON(t, tools.searchHistory(), `{"q":"tenant","states":["blocked"]}`).(map[string]any)
	items := blocked["items"].([]any)
	if blocked["total"].(float64) != 1 || len(items) != 1 {
		t.Fatalf("blocked search=%+v", blocked)
	}
	item := items[0].(map[string]any)
	if item["id"].(float64) != float64(intent) || item["blocked_reason"] != "second tenant unavailable" {
		t.Fatalf("blocked item=%+v", item)
	}

	page := callReadJSON(t, tools.searchHistory(), fmt.Sprintf(`{"q":"#%d"}`, digest)).(map[string]any)
	items = page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["summary"] != "authorization history body" {
		t.Fatalf("digest search=%+v", page)
	}
	if got := callReadJSON(t, tools.searchHistory(), `{"q":"tenant","limit":1}`).(map[string]any); got["total"].(float64) != 3 || !got["has_more"].(bool) || got["next_before"] == nil {
		t.Fatalf("bounded page=%+v", got)
	}
}

func TestSearchHistoryDefaultsToTenCompactEntries(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("compact history search", "compact history search", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	for i := 0; i < 12; i++ {
		if _, err := store.AddNode(db.KindFact, map[string]any{"summary": fmt.Sprintf("bounded history entry %d", i)}, 0, "confirmed", "worker", nil); err != nil {
			t.Fatal(err)
		}
	}
	tools := NewToolSet(store, "planner")
	first := callReadJSON(t, tools.searchHistory(), `{"q":"bounded history"}`).(map[string]any)
	if first["total"].(float64) != 12 || len(first["items"].([]any)) != 10 || !first["has_more"].(bool) || first["next_before"] == nil {
		t.Fatalf("default page=%+v", first)
	}
	second := callReadJSON(t, tools.searchHistory(), fmt.Sprintf(`{"q":"bounded history","before":%v}`, first["next_before"])).(map[string]any)
	if len(second["items"].([]any)) != 2 || second["has_more"].(bool) {
		t.Fatalf("second page=%+v", second)
	}
}

func TestCompactHistoryNodeUsesShortSentenceBoundary(t *testing.T) {
	prefix := fmt.Sprintf("网关路径绕过测试已完成全部否定，%0120d。", 0)
	payload := []byte(fmt.Sprintf(`{"summary":%q}`, prefix+fmt.Sprintf("后续字段%0200d", 0)))
	node := &db.Node{Kind: db.KindFact, Payload: payload}
	got := compactHistoryNode(node)
	if got["summary"] != prefix+"…(截断)" || got["summary_truncated"] != true {
		t.Fatalf("compact summary=%+v", got)
	}
}
