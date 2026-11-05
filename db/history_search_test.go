package db

import (
	"fmt"
	"testing"
)

func TestNodesBeforeWithSourcesFiltersPagesAndKeepsTotal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	source, err := d.CreateTask("history search source", "history search source", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("history search current", "history search current", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(current.ID); _ = d.DeleteTask(source.ID) })
	store := d.Exploration(current.ExplorationID)
	sourceStore := d.Exploration(source.ExplorationID)

	for i := 0; i < 3; i++ {
		if _, err := store.AddNode(KindFact, map[string]any{"summary": fmt.Sprintf("pagination marker %d", i)}, 0, "confirmed", "worker", nil); err != nil {
			t.Fatal(err)
		}
	}
	filter := NodeFilter{Kinds: []string{KindFact}, Query: "pagination marker"}
	first, hasMore, total, err := store.NodesBeforeWithSources(filter, 0, 2)
	if err != nil || !hasMore || total != 3 || len(first) != 2 {
		t.Fatalf("first page: nodes=%d hasMore=%v total=%d err=%v", len(first), hasMore, total, err)
	}
	second, hasMore, total, err := store.NodesBeforeWithSources(filter, first[len(first)-1].ID, 2)
	if err != nil || hasMore || total != 3 || len(second) != 1 {
		t.Fatalf("second page: nodes=%d hasMore=%v total=%d err=%v", len(second), hasMore, total, err)
	}

	terminal, err := sourceStore.AddIntent(map[string]any{"summary": "inherited completed marker"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.SetIntentState(terminal, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceStore.AddIntent(map[string]any{"summary": "inherited running marker"}, 1, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	nodes, _, total, err := store.NodesBeforeWithSources(NodeFilter{Kinds: []string{KindIntent}, Query: "inherited"}, 0, 20)
	if err != nil || total != 1 || len(nodes) != 1 || nodes[0].ID != terminal || !nodes[0].Inherited {
		t.Fatalf("source visibility: nodes=%+v total=%d err=%v", nodes, total, err)
	}
}

func TestNodesPageBooleanQuery(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("history boolean query", "history boolean query")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })
	store := d.Exploration(expID)

	alpha, err := store.AddNode(KindFact, map[string]any{"summary": "alpha prefix"}, 0, "confirmed", "omega-worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := store.AddNode(KindFact, map[string]any{"summary": "beta result"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	alphaOnly, err := store.AddNode(KindFact, map[string]any{"summary": "alpha without the other term"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	literal, err := store.AddNode(KindFact, map[string]any{"summary": "literal alpha%_ marker"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	assertQuery := func(query string, want ...int64) {
		t.Helper()
		nodes, total, err := store.NodesPage(NodeFilter{Kinds: []string{KindFact}, Query: query}, 1, 20)
		if err != nil {
			t.Fatal(err)
		}
		if total != len(want) || len(nodes) != len(want) {
			t.Fatalf("query %q: total=%d nodes=%d, want %d", query, total, len(nodes), len(want))
		}
		got := map[int64]bool{}
		for _, node := range nodes {
			got[node.ID] = true
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("query %q missed node %d: %+v", query, id, nodes)
			}
		}
	}
	assertQuery("alpha", alpha, alphaOnly, literal)
	assertQuery("  alpha   omega  ", alpha)
	assertQuery("beta OR alpha omega", beta, alpha)
	assertQuery("beta OR alpha", beta, alpha, alphaOnly, literal)
	assertQuery("alpha%_", literal)
	assertQuery(fmt.Sprintf("#%d", beta), beta)
	assertQuery("missing terms")

	first, total, err := store.nodesPage(NodeFilter{Kinds: []string{KindFact}, Query: "beta OR alpha"}, 1, 2, 0)
	if err != nil || total != 4 || len(first) != 2 {
		t.Fatalf("boolean first page: total=%d nodes=%d err=%v", total, len(first), err)
	}
	second, total, err := store.nodesPage(NodeFilter{Kinds: []string{KindFact}, Query: "beta OR alpha"}, 1, 2, first[len(first)-1].ID)
	if err != nil || total != 4 || len(second) != 2 {
		t.Fatalf("boolean second page: total=%d nodes=%d err=%v", total, len(second), err)
	}
}

func TestNodesPageAssetFilterIncludesCoveringDigest(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("history asset filter", "history asset filter")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })
	store := d.Exploration(expID)
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("history-%d.invalid", expID)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{assetID}) })

	fact, err := store.AddNode(KindFact, map[string]any{"summary": "anchored fact"}, 0, "confirmed", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.AddDigest(map[string]any{"title": "asset digest", "body": "anchored history"}, []int64{fact})
	if err != nil {
		t.Fatal(err)
	}
	otherAsset, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("other-history-%d.invalid", expID)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{otherAsset}) })
	if _, err := store.AddNode(KindFact, map[string]any{"summary": "other asset fact"}, 0, "confirmed", "worker", []int64{otherAsset}); err != nil {
		t.Fatal(err)
	}

	nodes, total, err := store.NodesPage(NodeFilter{AssetID: assetID}, 1, 20)
	if err != nil || total != 2 || len(nodes) != 2 {
		t.Fatalf("asset page: nodes=%+v total=%d err=%v", nodes, total, err)
	}
	seen := map[int64]bool{}
	for _, node := range nodes {
		seen[node.ID] = true
	}
	if !seen[fact] || !seen[digest] {
		t.Fatalf("asset filter missed fact or digest: %v", seen)
	}
}
