package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestMCPAssetProjection(t *testing.T) {
	asset := &db.Asset{ID: 17, Type: "endpoint", URL: "https://user:credential@example.invalid/login?token=secret#fragment", TaskIDs: []int64{9}, Extra: map[string]any{"note": "internal"}}
	data, err := json.Marshal(mcpInventoryAsset(asset))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "https://example.invalid/login") {
		t.Fatal(string(data))
	}
	for _, private := range []string{"credential", "secret", "fragment", "task_ids", "internal"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private field in %s", data)
		}
	}
}

func TestMCPInventoryAndProposalRoundTrip(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN is required for this database test")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	s, mem := mcpTestServer(t)
	s.m = &Manager{pg: pg}
	stamp := time.Now().UnixNano()
	company := func(suffix string) (int64, int64) {
		t.Helper()
		domain := fmt.Sprintf("proposal-%s-%d.example", suffix, stamp)
		c, _, err := pg.Companies().UpsertCompany(domain, "")
		if err != nil {
			t.Fatal(err)
		}
		// Cleanup before closing the local test database handle.
		var id int64
		err = pg.QueryRow(`INSERT INTO assets (type,company_id,domain) VALUES ('root_domain',$1,$2) RETURNING id`, c, domain).Scan(&id)
		if err != nil {
			_, _ = pg.Companies().DeleteCompanyWithAssets(c, true)
			t.Fatal(err)
		}
		return c, id
	}
	own, asset := company("a")
	defer pg.Companies().DeleteCompanyWithAssets(own, true)
	foreign, otherAsset := company("b")
	defer pg.Companies().DeleteCompanyWithAssets(foreign, true)
	token := mem.add(42, db.APITokenPrefix+"inventory", db.APIScopeAssetsRead, db.APIScopeTaskPropose)
	token.CompanyIDs = []int64{own}
	call := func(t *testing.T, name string, args map[string]any, wantError bool) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		if err != nil {
			t.Fatal(err)
		}
		res := decodeMCP(t, mcpPost(s, db.APITokenPrefix+"inventory", string(body)))
		if res.Error != nil || res.Result.IsError != wantError || len(res.Result.Content) == 0 {
			t.Fatalf("%s: %+v", name, res)
		}
		return res.Result.Content[0].Text
	}
	list := call(t, "list_company_assets", map[string]any{"company_id": own}, false)
	var inv struct{ Assets []mcpAssetRow }
	if json.Unmarshal([]byte(list), &inv) != nil || len(inv.Assets) != 1 || inv.Assets[0].ID != asset {
		t.Fatal(list)
	}
	call(t, "list_company_assets", map[string]any{"company_id": foreign}, true)
	const goal = "核对资产归属"
	input := map[string]any{"company_id": own, "asset_ids": []int64{otherAsset}, "goal": goal}
	call(t, "check_task_overlap", input, true)
	input["request_id"] = fmt.Sprintf("test-%d", stamp)
	call(t, "submit_task_draft", input, true)
	input["asset_ids"] = []int64{asset}
	call(t, "check_task_overlap", input, false)
	first := call(t, "submit_task_draft", input, false)
	if second := call(t, "submit_task_draft", input, false); second != first {
		t.Fatalf("retry generated a new draft: %s, %s", first, second)
	}
	input["goal"] = "另一个目的"
	call(t, "submit_task_draft", input, true)

	t.Run("cross-company source stays hidden in task summary", func(t *testing.T) {
		token.Scopes = append(token.Scopes, db.APIScopeRead)
		s.m.tasks = map[string]*Task{}
		record := func(company int64, description string, sources ...int64) int64 {
			t.Helper()
			task, err := pg.CreateTaskWithOptions(description, goal, db.TaskCreateOptions{
				CompanyIDs: []int64{company}, SourceTaskIDs: sources,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pg.DeleteTask(task.ID) })
			key := fmt.Sprint(task.ID)
			s.m.tasks[key] = &Task{ID: key, ExpID: task.ExplorationID, Store: pg.Exploration(task.ExplorationID), CompanyIDs: []int64{company}}
			return task.ID
		}
		local := record(own, "VISIBLE_SOURCE_NOTE")
		other := record(foreign, "PRIVATE_SOURCE_NOTE")
		current := record(own, "CURRENT_NOTE", local, other)
		payload := call(t, "get_task_graph", map[string]any{"task_id": current}, false)
		if strings.Contains(payload, "PRIVATE_SOURCE_NOTE") || !strings.Contains(payload, "VISIBLE_SOURCE_NOTE") {
			t.Fatalf("unexpected source visibility: %s", payload)
		}
	})
}
