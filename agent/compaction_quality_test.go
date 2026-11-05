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

func TestLineagePrepareBlockRejectsRepeatedlyTruncatedSummary(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("lineage rejects truncated summary", "lineage rejects truncated summary", nil, 0, 0)
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
	calls := 0
	c := NewCompactor(nil, "")
	c.prov = captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		calls++
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "incomplete model output"}, nil)
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "max_tokens"}, nil)
	}}
	b := block{Members: []int64{intent, fact}}
	if prepared, err := c.prepareBlock(context.Background(), store, g, b, nodes, versions, 1); err == nil || prepared != nil {
		t.Fatalf("truncated summary was prepared: %+v, %v", prepared, err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d want one retry", calls)
	}
	digests, err := store.ActiveDigests()
	if err != nil || len(digests) != 0 {
		t.Fatalf("failed compression hid source members: %+v, %v", digests, err)
	}
}

func TestLineageCompressionRetriesTruncationWithShorterPrompt(t *testing.T) {
	nodes := map[int64]*db.Node{
		1: {ID: 1, Kind: db.KindFact, State: "confirmed", Payload: json.RawMessage(`{"summary":"one"}`)},
		2: {ID: 2, Kind: db.KindFact, State: "confirmed", Payload: json.RawMessage(`{"summary":"two"}`)},
	}
	g := newColdGraph([]cgNode{{ID: 1, Kind: db.KindFact, State: "confirmed"}, {ID: 2, Kind: db.KindFact, State: "confirmed"}}, nil)
	calls := 0
	var budgets []int
	c := NewCompactor(nil, "")
	c.prov = captureUsageProvider{
		request: func(req llm.CompletionRequest) { budgets = append(budgets, req.MaxTokens) },
		stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
			calls++
			if calls == 1 {
				yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "incomplete"}, nil)
				yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "max_tokens"}, nil)
				return
			}
			yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "short complete summary"}, nil)
		},
	}
	body, err := c.compress(context.Background(), g, block{Members: []int64{1, 2}}, nodes)
	if err != nil || body != "short complete summary" || calls != 2 {
		t.Fatalf("body=%q calls=%d err=%v", body, calls, err)
	}
	if len(budgets) != 2 || budgets[0] != 600 || budgets[1] != 900 {
		t.Fatalf("retry budgets=%v", budgets)
	}
	if !strings.Contains(compressionSystemPrompt, "主题：结论") || !strings.Contains(compressionRetryPrompt, "主题：结论") {
		t.Fatal("compression prompts do not require decision-oriented titles")
	}
}
