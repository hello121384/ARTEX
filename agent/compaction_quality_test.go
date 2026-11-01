package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestDigestTitleFollowsBodyRatherThanUnrelatedAnchor(t *testing.T) {
	digest := &db.Node{Payload: json.RawMessage(`{"title":"WAF encoding bypass","body":"XXE external entity tests were negative. No file was returned."}`)}
	if got := digestCardTitle(digest); !strings.HasPrefix(got, "XXE external entity tests were negative") {
		t.Fatalf("title=%q", got)
	}
	if got := digestTopicTitle(""); got != "历史探索分组" {
		t.Fatalf("empty title=%q", got)
	}
	payload := digestPayload("订单归属校验已完成。未发现越权写。", block{Members: []int64{1, 2}, Anchors: []int64{3}}, map[int64]*db.Node{
		1: {ID: 1, Kind: db.KindIntent}, 2: {ID: 2, Kind: db.KindFact}, 3: {ID: 3, Kind: db.KindIntent},
	}, 1, "sig")
	if payload["title"] != "订单归属校验已完成" {
		t.Fatalf("payload title=%+v", payload["title"])
	}
}

func TestLineagePrepareBlockUsesSourceExcerptAfterTruncation(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("lineage deterministic fallback", "lineage deterministic fallback", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "verify order authorization"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetIntentState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	fact, err := store.AddNode(db.KindFact, map[string]any{"summary": "cross tenant order returned 15120"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Link(intent, db.RelYields, fact); err != nil {
		t.Fatal(err)
	}
	g, nodes, err := loadColdGraph(store)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.ContentVersions()
	if err != nil {
		t.Fatal(err)
	}
	c := NewCompactor(nil, "")
	c.prov = captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "incomplete model output"}, nil)
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "max_tokens"}, nil)
	}}
	b := block{Members: []int64{intent, fact}}
	prepared, err := c.prepareBlock(context.Background(), store, g, b, nodes, versions, 1)
	if err != nil {
		t.Fatal(err)
	}
	body := prepared.payload["body"].(string)
	for _, want := range []string{"verify order authorization", "cross tenant order returned 15120"} {
		if !strings.Contains(body, want) {
			t.Fatalf("fallback body missing %q: %s", want, body)
		}
	}
	if len([]rune(body)) > 220 || prepared.payload["title"] != "工作：verify order authorization" {
		t.Fatalf("fallback payload=%+v", prepared.payload)
	}
}
