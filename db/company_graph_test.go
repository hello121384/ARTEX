package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCompanyGraphLazyBranches(t *testing.T) {
	d, store, companies := testSetup(t)
	defer d.Close()
	stamp := time.Now().UnixNano()
	company, _, err := companies.UpsertCompany(fmt.Sprintf("graph fixture %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	defer companies.DeleteCompanyWithAssets(company, true)
	root := fmt.Sprintf("a-%d.example", stamp)
	virtual := fmt.Sprintf("z-%d.example", stamp)
	add := func(kind string, cols map[string]any) string {
		cols["company_id"] = company
		return assetKey(seedTreeAsset(t, d, kind, cols))
	}
	rootKey := add("root_domain", map[string]any{"domain": root})
	sub := "www." + root
	subKey := add("subdomain", map[string]any{"domain": sub, "root_domain": root})
	serviceKey := add("service", map[string]any{"domain": sub, "root_domain": root, "port": 443, "url": "https://" + sub, "service_type": "http"})
	otherService := add("service", map[string]any{"domain": sub, "root_domain": root, "port": 80, "url": "http://" + sub, "service_type": "http"})
	endpointKey := add("endpoint", map[string]any{"domain": sub, "root_domain": root, "port": 80, "url": "http://" + sub + "/health", "method": "GET"})
	legacyKey := add("endpoint", map[string]any{"root_domain": root, "url": "http://" + sub + "/legacy", "method": "GET"})
	explicitPortKey := add("endpoint", map[string]any{"root_domain": root, "url": "https://" + sub + ":80/legacy", "method": "GET"})
	virtualSub := add("subdomain", map[string]any{"domain": "mail." + virtual, "root_domain": virtual})
	appKey := add("app", map[string]any{"app_name": root})
	foreignID := seedTreeAsset(t, d, "subdomain", map[string]any{"domain": "unassigned." + root, "root_domain": root})
	defer deleteAsset(d, foreignID)

	ctx := context.Background()
	check := func(group int64, parent string, offset, limit int, more bool, expected ...CompanyGraphNode) {
		t.Helper()
		page, err := store.ListCompanyGraphChildren(ctx, group, parent, offset, limit)
		if err != nil {
			t.Fatalf("parent %q: %v", parent, err)
		}
		if page.HasMore != more || len(page.Nodes) != len(expected) {
			t.Fatalf("parent %q: got %+v, want %+v has_more=%t", parent, page, expected, more)
		}
		for i := range expected {
			if page.Nodes[i] != expected[i] {
				t.Errorf("parent %q: node %d got %+v, want %+v", parent, i, page.Nodes[i], expected[i])
			}
		}
	}
	check(company, "", 0, 1, true, CompanyGraphNode{rootKey, "root_domain", root, true})
	check(company, "", 1, 1, false, CompanyGraphNode{"r:" + virtual, "root_domain", virtual, true})
	check(company, rootKey, 0, 25, false, CompanyGraphNode{subKey, "subdomain", sub, true})
	check(company, "r:"+virtual, 0, 25, false, CompanyGraphNode{virtualSub, "subdomain", "mail." + virtual, false})
	check(company, subKey, 0, 25, false,
		CompanyGraphNode{serviceKey, "service", "https://" + sub, false},
		CompanyGraphNode{otherService, "service", "http://" + sub, true})
	check(company, otherService, 0, 25, false, CompanyGraphNode{endpointKey, "endpoint", "http://" + sub + "/health", false},
		CompanyGraphNode{legacyKey, "endpoint", "http://" + sub + "/legacy", false},
		CompanyGraphNode{explicitPortKey, "endpoint", "https://" + sub + ":80/legacy", false})
	check(company, "company", 0, 25, false, CompanyGraphNode{appKey, "app", root, false})
	check(0, "r:"+root, 0, 25, false, CompanyGraphNode{assetKey(foreignID), "subdomain", "unassigned." + root, false})

	for _, c := range []struct {
		group  int64
		parent string
		want   error
	}{
		{company, assetKey(foreignID), sql.ErrNoRows}, {0, rootKey, sql.ErrNoRows},
		{company, "a:-8", ErrInvalidGraphParent}, {company, "r:missing.invalid", sql.ErrNoRows},
	} {
		if _, err := store.ListCompanyGraphChildren(ctx, c.group, c.parent, 0, 25); !errors.Is(err, c.want) {
			t.Errorf("group %d parent %q: got %v want %v", c.group, c.parent, err, c.want)
		}
	}
}
