package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestPlannerHistoryReadDedupState(t *testing.T) {
	planner := NewToolSet(nil, "planner")
	if _, duplicate := planner.reusePlannerHistoryRead("node_detail", "7"); duplicate {
		t.Fatal("first read must run")
	}
	result, duplicate := planner.reusePlannerHistoryRead("node_detail", "7")
	if !duplicate || !strings.Contains(result.Flatten(), "直接使用该工具上次的返回") {
		t.Fatalf("duplicate read was not replaced by a short response: %s", result.Flatten())
	}
	if _, duplicate := planner.reusePlannerHistoryRead("node_detail", "8"); duplicate {
		t.Fatal("a different node must run")
	}
	if got := planner.HistoryReadStats(); got.Calls != 3 || got.Unique != 2 || got.Repeated != 1 {
		t.Fatalf("stats=%+v", got)
	}

	worker := NewToolSet(nil, "worker")
	for i := 0; i < 2; i++ {
		if _, duplicate := worker.reusePlannerHistoryRead("node_detail", "7"); duplicate {
			t.Fatal("worker reads must remain unchanged")
		}
	}
	if got := worker.HistoryReadStats(); got.Calls != 0 {
		t.Fatalf("worker stats=%+v", got)
	}
}

func TestPlannerHistoryReadAllowsRetryAfterFailure(t *testing.T) {
	tools := NewToolSet(nil, "planner")
	if _, duplicate := tools.reusePlannerHistoryRead("search_history", "q"); duplicate {
		t.Fatal("first attempt must run")
	}
	tools.retryPlannerHistoryRead("search_history", "q")
	if _, duplicate := tools.reusePlannerHistoryRead("search_history", "q"); duplicate {
		t.Fatal("failed read must remain retryable")
	}
	if got := tools.HistoryReadStats(); got.Calls != 2 || got.Unique != 1 || got.Repeated != 0 {
		t.Fatalf("stats=%+v", got)
	}
}

func TestPlannerHistoryToolsDeduplicateEquivalentCalls(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("history read dedup", "history read dedup", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	fact, err := store.AddNode(db.KindFact, map[string]any{"summary": "dedup marker"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.AddDigest(map[string]any{"body": "dedup history"}, []int64{fact})
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(store, "planner")
	callReadJSON(t, tools.searchHistory(), `{"q":" dedup marker ","kinds":["fact","intent"]}`)
	secondSearch := callReadJSON(t, tools.searchHistory(), `{"kinds":["intent","fact"],"q":"dedup marker"}`).(map[string]any)
	if secondSearch["duplicate"] != true {
		t.Fatalf("equivalent search was not deduplicated: %+v", secondSearch)
	}
	callReadJSON(t, tools.nodeDetail(), fmt.Sprintf(`{"id":%d}`, fact))
	secondDetail := callReadJSON(t, tools.nodeDetail(), fmt.Sprintf(`{"id":"%d"}`, fact)).(map[string]any)
	if secondDetail["duplicate"] != true {
		t.Fatalf("equivalent node detail was not deduplicated: %+v", secondDetail)
	}
	callReadJSON(t, tools.expandDigest(), fmt.Sprintf(`{"id":%d}`, digest))
	secondExpand := callReadJSON(t, tools.expandDigest(), fmt.Sprintf(`{"id":%d}`, digest)).(map[string]any)
	if secondExpand["duplicate"] != true {
		t.Fatalf("equivalent digest expansion was not deduplicated: %+v", secondExpand)
	}
	if got := tools.HistoryReadStats(); got.Calls != 6 || got.Unique != 3 || got.Repeated != 3 {
		t.Fatalf("stats=%+v", got)
	}
}
