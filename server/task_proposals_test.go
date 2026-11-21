package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestProposalOperatorReviewHTTP(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	manager := &Manager{pg: pg, tasks: map[string]*Task{}}
	s := newAdmissionTestServer(manager, func(*Task) bool { return false })
	s.jwtKey = []byte("test-review-secret")
	defer restoreConcurrencySetting(t, manager)()
	if err := manager.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.registerProposalRoutes(mux)
	h := s.requireAuth(mux)
	jwt, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, method, path, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body.String())
		}
		var data map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	base := "/api/task-proposals"
	call("artex_mcp_inventory", "GET", base, "", 401)
	call("artex_mcp_inventory", "POST", base+"/1/confirm", `{"version":1,"review_key":"test"}`, 401)
	name := fmt.Sprintf("review-http-%d", time.Now().UnixNano())
	company, _, err := pg.Companies().UpsertCompany(name, "")
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Companies().DeleteCompanyWithAssets(company, true)
	var asset int64
	if err := pg.QueryRow("INSERT INTO assets(type,company_id,domain) VALUES ('root_domain',$1,$2) RETURNING id", company, name+".invalid").Scan(&asset); err != nil {
		t.Fatal(err)
	}
	in := db.ProposalInput{CompanyID: company, Goal: "复核资产归属", AssetIDs: []int64{asset}}
	p, err := pg.SubmitTaskProposal(t.Context(), 88, name, in, []int64{company})
	if err != nil {
		t.Fatal(err)
	}
	path := base + "/" + strconv.FormatInt(p.ID, 10)
	page := call(jwt, "GET", base+"?limit=1", "", 200)
	if _, ok := page["proposals"]; !ok {
		t.Fatalf("no list: %v", page)
	}
	review := call(jwt, "GET", path, "", 200)
	if review["review_key"] == "" || review["overlap"] == nil {
		t.Fatalf("review missing: %v", review)
	}
	conflict := call(jwt, "POST", path+"/confirm", `{"version":1,"review_key":"stale"}`, 409)
	if conflict["code"] != "proposal_conflict" {
		t.Fatalf("conflict code missing: %v", conflict)
	}
	call(jwt, "PATCH", path, fmt.Sprintf(`{"version":1,"goal":"复核归属记录","asset_ids":[%d]}`, asset), 200)
	call(jwt, "POST", path+"/withdraw", `{"version":2}`, 200)
	closed := call(jwt, "GET", path, "", 200)
	if closed["overlap"] != nil || closed["review_key"] != "" {
		t.Fatalf("withdrawn draft was confirmable: %v", closed)
	}

	next, err := pg.SubmitTaskProposal(t.Context(), 88, name+"-confirmed", in, []int64{company})
	if err != nil {
		t.Fatal(err)
	}
	path = base + "/" + strconv.FormatInt(next.ID, 10)
	review = call(jwt, "GET", path, "", 200)
	body := fmt.Sprintf(`{"version":1,"review_key":"%s"}`, review["review_key"])
	accepted := call(jwt, "POST", path+"/confirm", body, 200)
	id, ok := accepted["task_id"].(string)
	if !ok {
		t.Fatalf("missing task id: %v", accepted)
	}
	task, ok := manager.Task(id)
	if !ok || !task.lifecycleSnapshot().Queued || s.engine.Started(id) {
		t.Fatalf("expected confirmed task waiting for capacity: %s", id)
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	defer pg.DeleteTask(n)
	retry := call(jwt, "POST", path+"/confirm", body, 200)
	if retry["task_id"] != id {
		t.Fatalf("retry created a second task: %v", retry)
	}
}
