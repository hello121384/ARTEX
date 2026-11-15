package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestWorkerContextDataRetrievesOldDirectParentAndAnchoredFact(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("old parent worker context", "verify linked old evidence", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	asset, err := d.Assets().UpsertRootDomain(db.UpsertRootDomainReq{
		Domain: fmt.Sprintf("worker-context-%d.invalid", task.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{asset}) })

	parent, err := store.AddNode(db.KindFact, map[string]any{
		"summary": "OLD DIRECT PARENT FACT MUST BE RESTORED", "confidence": "observed",
	}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AddNode(db.KindFact, map[string]any{
		"summary": "OLD SAME ASSET FACT MUST BE RESTORED", "confidence": "observed",
	}, 0, "confirmed", "worker", []int64{asset})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 75; i++ {
		if _, err := store.AddNode(db.KindFact, map[string]any{"summary": fmt.Sprintf("UNRELATED LATER FACT %d", i)},
			0, "confirmed", "worker", nil); err != nil {
			t.Fatal(err)
		}
	}
	intentID, err := store.AddIntent(map[string]any{
		"summary":   "review previously discovered issue",
		"asset_ids": []int64{asset},
	}, 5, []int64{asset}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Link(parent, db.RelDerivedFrom, intentID); err != nil {
		t.Fatal(err)
	}
	intent, err := store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := NewToolSet(store, "worker").workerContextData(intent)
	if err != nil {
		t.Fatal(err)
	}
	got := renderWorkerTaskContext(data, intent)
	for _, want := range []string{
		"OLD DIRECT PARENT FACT MUST BE RESTORED",
		"OLD SAME ASSET FACT MUST BE RESTORED",
		"verify linked old evidence",
		"search_history(asset_id/q)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("scoped Worker context missed %q: %s", want, got)
		}
	}
	if strings.Contains(got, "UNRELATED LATER FACT") {
		t.Fatalf("scoped worker included unrelated recent facts: %s", got)
	}
}

func TestWorkerContextDataRetrievesInheritedAssetEvidence(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	source, err := d.CreateTask("worker source", "source task", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("worker child", "child task",
		db.TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(current.ID); _ = d.DeleteTask(source.ID) })
	asset, err := d.Assets().UpsertRootDomain(db.UpsertRootDomainReq{
		Domain: fmt.Sprintf("worker-source-%d.invalid", source.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{asset}) })
	parentStore := d.Exploration(source.ExplorationID)
	if _, err := parentStore.AddNode(db.KindFact,
		map[string]any{"summary": "INHERITED ASSET FACT MUST SURVIVE"},
		0, "confirmed", "worker", []int64{asset}); err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(current.ExplorationID)
	intentID, err := store.AddIntent(map[string]any{
		"summary": "child asset analysis", "asset_ids": []int64{asset},
	}, 5, []int64{asset}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := NewToolSet(store, "worker").workerContextData(intent)
	if err != nil {
		t.Fatal(err)
	}
	got := renderWorkerTaskContext(data, intent)
	if !strings.Contains(got, "INHERITED ASSET FACT MUST SURVIVE") {
		t.Fatalf("direct source evidence was not retrieved: %s", got)
	}
}

func TestWorkerContextDataAnchorlessIntentFallsBack(t *testing.T) {
	intent := &db.Node{ID: 101, Payload: []byte("{\"summary\":\"assetless discovery\"}")}
	if _, err := NewToolSet(nil, "worker").workerContextData(intent); err == nil {
		t.Fatal("anchorless Worker context must be rejected and use original full-graph fallback")
	}
}
