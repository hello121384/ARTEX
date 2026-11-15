package agent

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Autumn-27/artex/db"
)

// workerContextData retrieves decision-relevant history BEFORE presentation truncation.
// Unlike graphOverviewData it never loads the entire cold graph, and direct parent
// facts are retrieved by ID even when outside all recent-N windows.
func (t *ToolSet) workerContextData(intent *db.Node) (map[string]any, error) {
	if t.ts == nil || intent == nil {
		return nil, fmt.Errorf("missing worker exploration or intent")
	}
	assets := intentAssetIDs(intent)
	// Without asset anchors, relevance cannot be determined safely from a bounded
	// candidate query. The caller must fall back to the original full overview.
	if len(assets) == 0 {
		return nil, fmt.Errorf("intent #%d has no asset_ids for scoped retrieval", intent.ID)
	}
	// A giant multi-asset intent would multiply indexed history/COUNT queries
	// and make any bounded selection incomplete. Preserve the original view.
	if len(assets) > 16 {
		return nil, fmt.Errorf("intent #%d has %d asset_ids; using full overview", intent.ID, len(assets))
	}
	sources, err := t.ts.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	type scope struct {
		store  *db.ExplorationStore
		taskID int64
	}
	scopes := []scope{{store: t.ts}}
	scopeByTask := map[int64]*db.ExplorationStore{0: t.ts}
	for _, source := range sources {
		scopes = append(scopes, scope{store: source.Store, taskID: source.Task.TaskID})
		scopeByTask[source.Task.TaskID] = source.Store
	}
	candidates := map[int64]*db.Node{intent.ID: intent}
	anchored := map[int64]map[int64]bool{}
	add := func(n *db.Node, asset int64) {
		if n == nil {
			return
		}
		if _, ok := candidates[n.ID]; !ok {
			candidates[n.ID] = n
		}
		if asset > 0 {
			if anchored[n.ID] == nil {
				anchored[n.ID] = map[int64]bool{}
			}
			anchored[n.ID][asset] = true
		}
	}
	// Always include direct parents/results of the active intent, even if they
	// are old, have no asset anchors, or were folded into a cold Digest.
	direct, err := t.ts.EdgesTouching([]int64{intent.ID})
	if err != nil {
		return nil, err
	}
	mandatory := map[int64]bool{intent.ID: true}
	for _, edge := range direct {
		if edge.From == intent.ID && (edge.Rel == db.RelYields || edge.Rel == db.RelDerivedFrom || edge.Rel == db.RelSpawns) {
			mandatory[edge.To] = true
		}
		if edge.To == intent.ID && (edge.Rel == db.RelDerivedFrom || edge.Rel == db.RelSpawns) {
			mandatory[edge.From] = true
		}
	}
	for id := range mandatory {
		if id == intent.ID {
			continue
		}
		n, err := t.ts.GetNodeWithSources(id)
		if err != nil {
			return nil, err
		}
		add(n, 0)
	}
	// Candidates are selected in SQL by actual asset anchors, not from the
	// previous recent_facts/recent_done_intents windows. Source tasks remain
	// read-only and only their terminal intents are inherited.
	const perAsset = 48
	for _, asset := range assets {
		if asset <= 0 {
			continue
		}
		hits, _, _, err := t.ts.NodesBeforeWithSources(db.NodeFilter{
			Kinds:   []string{db.KindIntent, db.KindFact, db.KindFinding, db.KindDigest},
			AssetID: asset,
		}, 0, perAsset)
		if err != nil {
			return nil, err
		}
		for _, n := range hits {
			add(n, asset)
		}
	}

	// Retrieve lineage on the selected IDs (including a historical producer
	// intent for a direct parent Fact), rather than truncating the global edge
	// table to an unordered 5,000 rows.
	edges := []db.Edge{}
	seedIDs := map[int64]bool{}
	for id := range mandatory {
		seedIDs[id] = true
	}
	for _, sc := range scopes {
		ids := []int64{}
		for _, n := range candidates {
			if n.Inherited {
				if n.SourceTaskID != sc.taskID {
					continue
				}
			} else if sc.taskID != 0 {
				continue
			}
			ids = append(ids, n.ID)
		}
		if len(ids) == 0 {
			continue
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		batch, err := sc.store.EdgesTouching(ids)
		if err != nil {
			return nil, err
		}
		edges = append(edges, batch...)
		// Sources only contribute evidence, not runnable work.
		for _, e := range batch {
			if e.Rel != db.RelYields && e.Rel != db.RelDerivedFrom && e.Rel != db.RelSpawns {
				continue
			}
			if seedIDs[e.To] && e.Rel == db.RelYields {
				seedIDs[e.From] = true
			}
		}
	}
	// One-hop producer intent for direct parent Fact(s).
	for id := range seedIDs {
		if candidates[id] != nil {
			continue
		}
		n, err := t.ts.GetNodeWithSources(id)
		if err != nil {
			return nil, err
		}
		add(n, 0)
	}
	parents := map[int64][]int64{}
	yields := map[int64][]int64{}
	fromIntent := map[int64]int64{}
	for _, e := range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns:
			parents[e.To] = append(parents[e.To], e.From)
		case db.RelYields:
			yields[e.From] = append(yields[e.From], e.To)
			fromIntent[e.To] = e.From
		}
	}
	// Build separate local and inherited rows. Order mandatory lineage first,
	// then newer history; never let a same-asset recent window evict a parent.
	ordered := make([]*db.Node, 0, len(candidates))
	for _, n := range candidates {
		ordered = append(ordered, n)
	}
	sort.Slice(ordered, func(i, j int) bool {
		ai, aj := mandatory[ordered[i].ID], mandatory[ordered[j].ID]
		if ai != aj {
			return ai
		}
		return ordered[i].ID > ordered[j].ID
	})

	data := map[string]any{
		"open_intents":        []map[string]any{},
		"running_intents":     []map[string]any{},
		"recent_done_intents": []map[string]any{},
		"recent_facts":        []map[string]any{},
		"finding_list":        []map[string]any{},
		"cold_digests":        []map[string]any{},
	}
	if description, goal, err := t.ts.Root(); err == nil {
		data["task"] = map[string]any{"description": description, "goal": goal}
	} else {
		return nil, err
	}
	related := map[int64]map[string]any{}
	for _, src := range sources {
		related[src.Task.TaskID] = map[string]any{
			"source_task_id":        src.Task.TaskID,
			"recent_intent_results": []map[string]any{},
			"recent_facts":          []map[string]any{},
			"recent_findings":       []map[string]any{},
			"cold_digests":          []map[string]any{},
		}
	}
	// Preserve active human hints, even when the current asset has no
	// relationship to the hint's original node.
	localHints, err := scopedWorkerHints(t.ts, 50)
	if err != nil {
		return nil, err
	}
	data["hints"] = localHints
	for _, src := range sources {
		hints, err := scopedWorkerHints(src.Store, 12)
		if err != nil {
			return nil, err
		}
		related[src.Task.TaskID]["hints"] = hints
	}
	digestsByTask := map[int64][]*db.Node{}
	for _, n := range ordered {
		row := compactNode(n)
		if n.Kind == db.KindIntent {
			row = compactIntents([]*db.Node{n}, parents, yields)[0]
		}
		if n.Inherited {
			inheritedMap(row, n.SourceTaskID)
		}
		if matched := anchored[n.ID]; len(matched) > 0 {
			ids := make([]int64, 0, len(matched))
			for id := range matched {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			row["asset_ids"] = ids
		}
		if parent := fromIntent[n.ID]; parent > 0 {
			row["from_intent"] = parent
		}
		var dest map[string]any = data
		if n.Inherited {
			dest = related[n.SourceTaskID]
			if dest == nil {
				continue
			}
		}
		switch n.Kind {
		case db.KindIntent:
			key := "recent_done_intents"
			if n.Inherited {
				key = "recent_intent_results"
			} else if n.State == "open" {
				key = "open_intents"
			} else if n.State == "running" {
				key = "running_intents"
			}
			dest[key] = append(dest[key].([]map[string]any), row)
		case db.KindFact:
			dest["recent_facts"] = append(dest["recent_facts"].([]map[string]any), row)
		case db.KindFinding:
			key := "finding_list"
			if n.Inherited {
				key = "recent_findings"
			}
			dest[key] = append(dest[key].([]map[string]any), row)
		case db.KindDigest:
			if n.State == db.StateDigestActive {
				group := int64(0)
				if n.Inherited {
					group = n.SourceTaskID
				}
				digestsByTask[group] = append(digestsByTask[group], n)
			}
		}
	}
	for group, selected := range digestsByTask {
		store := scopeByTask[group]
		if store == nil {
			continue
		}
		view := loadLineageDigestView(store, selected, false)
		cards, _, _ := coldDigestsRecentView(view, len(selected))
		var dest map[string]any = data
		if group > 0 {
			dest = related[group]
		}
		for _, card := range cards {
			if group > 0 {
				inheritedMap(card, group)
			}
			if matched := anchored[workerContextID(card["id"])]; len(matched) > 0 {
				ids := make([]int64, 0, len(matched))
				for id := range matched {
					ids = append(ids, id)
				}
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
				card["asset_ids"] = ids
			}
		}
		dest["cold_digests"] = cards
	}
	rows := make([]map[string]any, 0, len(sources))
	for _, src := range sources {
		rows = append(rows, related[src.Task.TaskID])
	}
	data["related_tasks"] = rows
	return data, nil
}

func scopedWorkerHints(store *db.ExplorationStore, cap int) ([]map[string]any, error) {
	nodes, err := store.ListByKind(db.KindHint, cap)
	if err != nil {
		return nil, err
	}
	hints := []map[string]any{}
	for _, n := range nodes {
		if n.State != "active" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(n.Payload, &payload); err != nil {
			continue
		}
		hint := map[string]any{"id": n.ID, "state": n.State, "text": payload["text"]}
		if payload["traffic_refs"] != nil {
			hint["traffic_refs"] = payload["traffic_refs"]
		}
		hints = append(hints, hint)
	}
	return hints, nil
}
