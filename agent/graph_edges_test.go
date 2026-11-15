package agent

import (
	"testing"
)

// Previously graphOverviewData called Edges(5000) with an unordered LIMIT,
// silently omitting valid yields/parents on larger tasks.
func TestGraphOverviewDoesNotTruncateLineageAt5000Edges(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("large edge graph", "preserve all visible lineage", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	id, err := store.AddIntent(map[string]any{"summary": "producer with many outcomes"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetIntentState(id, "running"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(
		"INSERT INTO exploration_nodes (exploration_id,kind,payload,priority,state,origin) "+
			"SELECT $1,'fact','{}',0,'confirmed','worker' FROM generate_series(1,5101)",
		task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(
		"INSERT INTO exploration_edges (exploration_id,src_id,rel,dst_id) "+
			"SELECT $1,$2,'yields',id FROM exploration_nodes "+
			"WHERE exploration_id=$1 AND kind='fact' AND id<>$2 ORDER BY id DESC LIMIT 5101 "+
			"ON CONFLICT DO NOTHING",
		task.ExplorationID, id); err != nil {
		t.Fatal(err)
	}
	overview := NewToolSet(store, "planner").graphOverviewData()
	running := ovMaps(overview["running_intents"])
	var found bool
	for _, row := range running {
		if workerContextID(row["id"]) != id {
			continue
		}
		found = true
		if got := len(workerContextIDs(row["yields"])); got != 5101 {
			t.Errorf("graph overview lost lineage edges: got %d, want 5101", got)
		}
	}
	if !found {
		t.Fatalf("running intent #%d missing", id)
	}
}
