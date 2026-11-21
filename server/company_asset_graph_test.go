package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestCompanyAssetGraphHTTP(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	server := New(t.Context(), m, dir, dir, dir)
	handler := server.Handler()
	token, err := signJWT(server.jwtKey)
	if err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	companyID, _, err := m.pg.Companies().UpsertCompany(fmt.Sprintf("graph HTTP %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.Companies().DeleteCompanyWithAssets(companyID, true)
	domain := fmt.Sprintf("graph%d.invalid", stamp)
	_, err = m.pg.Exec(`INSERT INTO assets(type, domain, root_domain, company_id)
VALUES ('root_domain',$1,$1,$2), ('subdomain','app.' || $1,$1,$2)`, domain, companyID)
	if err != nil {
		t.Fatal(err)
	}

	get := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("GET %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		return rec
	}
	path := fmt.Sprintf("/api/companies/%d/asset-graph", companyID)
	readPage := func(query string) *db.CompanyGraphPage {
		t.Helper()
		var page db.CompanyGraphPage
		if err := json.Unmarshal(get(path+query, http.StatusOK).Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return &page
	}
	page := readPage("?limit=1")
	if len(page.Nodes) != 1 || page.Nodes[0].Kind != "root_domain" || page.Nodes[0].Label != domain || !page.Nodes[0].Expandable || page.HasMore {
		t.Fatalf("unexpected roots: %+v", page)
	}
	children := readPage("?parent=" + url.QueryEscape(page.Nodes[0].Key))
	if len(children.Nodes) != 1 || children.Nodes[0].Kind != "subdomain" || children.Nodes[0].Label != "app."+domain {
		t.Fatalf("unexpected children: %+v", children)
	}
	if next := readPage("?offset=1"); len(next.Nodes) != 0 || next.HasMore {
		t.Fatalf("unexpected next page: %+v", next)
	}
	for _, query := range []string{"?limit=101", "?offset=-1", "?parent=bad"} {
		get(path+query, http.StatusBadRequest)
	}
	get("/api/companies/0/asset-graph", http.StatusBadRequest)
	get("/api/companies/unassigned/asset-graph?limit=1", http.StatusOK)
	get(path+"?parent=a:9223372036854775807", http.StatusNotFound)
	get("/api/companies/unassigned/asset-graph?parent="+url.QueryEscape(page.Nodes[0].Key), http.StatusNotFound)
}
