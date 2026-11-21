package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestTaskProposalLifecycleAndOverlap(t *testing.T) {
	d, _, cs := testSetup(t)
	defer d.Close()
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	company := func(s string) int64 {
		id, _, err := cs.UpsertCompany(fmt.Sprintf("proposal-%s-%d", s, stamp), "")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b := company("a"), company("b")
	defer cs.DeleteCompanyWithAssets(a, true)
	defer cs.DeleteCompanyWithAssets(b, true)
	asset := func(prefix string, co int64) int64 {
		return seedTreeAsset(t, d, "root_domain", map[string]any{"company_id": co, "domain": fmt.Sprintf("%s-%d.invalid", prefix, stamp)})
	}
	x, y, z := asset("x", a), asset("y", a), asset("z", b)
	allowed := []int64{a}
	in := ProposalInput{CompanyID: a, Goal: "Review asset ownership", AssetIDs: []int64{y, x, x}}
	submit := func(key string, in ProposalInput) (*TaskProposal, error) {
		return d.SubmitTaskProposal(ctx, stamp, key, in, allowed)
	}
	for _, bad := range []ProposalInput{{CompanyID: a, Goal: "x", AssetIDs: []int64{x, z}}, {CompanyID: b, Goal: "x", AssetIDs: []int64{z}}} {
		if _, err := submit("forbidden", bad); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("cross-company submit: %v", err)
		}
		if _, err := CheckProposalOverlap(ctx, d, bad, allowed, 0); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("cross-company overlap: %v", err)
		}
	}
	type outcome struct {
		p   *TaskProposal
		err error
	}
	ch := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() { p, err := submit("req", in); ch <- outcome{p, err} }()
	}
	first, second := <-ch, <-ch
	if first.err != nil || second.err != nil {
		t.Fatal(first.err, second.err)
	}
	p := first.p
	if p.ID != second.p.ID || p.Status != "pending" || len(p.Assets) != 2 || !slices.Equal(p.AssetIDs, []int64{x, y}) {
		t.Fatalf("retry and normalized input: %+v %+v", p, second.p)
	}
	overlap := func(exclude int64) *ProposalOverlap {
		r, err := CheckProposalOverlap(ctx, d, in, allowed, exclude)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	self := overlap(p.ID)
	if len(self.Matches) != 0 || !slices.Equal(self.UnmatchedAssetIDs, []int64{x, y}) {
		t.Fatalf("self matched: %+v", self)
	}
	smaller := ProposalInput{CompanyID: a, Goal: "review   ASSET ownership", AssetIDs: []int64{y}}
	if _, err := submit("req", smaller); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("same key, new content: %v", err)
	}
	p2, err := submit("req2", smaller)
	if err != nil {
		t.Fatal(err)
	}
	check := overlap(p.ID)
	if len(check.Matches) != 1 || !check.Matches[0].ExactGoal || !slices.Equal(check.Matches[0].ExplicitAssetIDs, []int64{y}) || !slices.Equal(check.UnmatchedAssetIDs, []int64{x}) || check.ReviewKey == self.ReviewKey {
		t.Fatalf("partial: %+v", check)
	}
	if _, err := d.EditTaskProposal(ctx, p2.ID, p2.Version, in); !errors.Is(err, ErrProposalInvalid) {
		t.Fatalf("expanded scope: %v", err)
	}
	edited := smaller
	edited.Goal = "verify owner"
	p2, err = d.EditTaskProposal(ctx, p2.ID, p2.Version, edited)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.WithdrawTaskProposal(ctx, p2.ID, p2.Version-1); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := d.WithdrawTaskProposal(ctx, p2.ID, p2.Version); err != nil {
		t.Fatal(err)
	}
	if overlap(p.ID).ReviewKey != self.ReviewKey {
		t.Fatal("withdrawn proposal still contributes")
	}

	makeTask := func(name string, companyIDs []int64, sources []int64) *Task {
		task, err := d.CreateTaskWithOptions(name, in.Goal, TaskCreateOptions{CompanyIDs: companyIDs, SourceTaskIDs: sources})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	hidden := makeTask("other company", []int64{a, b}, nil)
	defer d.DeleteTask(hidden.ID)
	prior := makeTask("historic scope", allowed, []int64{hidden.ID})
	defer d.DeleteTask(prior.ID)
	for _, target := range []struct {
		task  *Task
		asset int64
	}{{hidden, x}, {prior, y}} {
		_, err := d.Exploration(target.task.ExplorationID).AddNode(KindFact, map[string]any{"summary": "ownership record"}, 0, "confirmed", "worker", []int64{target.asset})
		if err != nil {
			t.Fatal(err)
		}
	}
	check = overlap(p.ID)
	if len(check.Matches) != 1 {
		t.Fatalf("hidden source or missing task: %+v", check)
	}
	m := check.Matches[0]
	if m.ID != prior.ID || !m.ExactGoal || !slices.Equal(m.AssetIDs, []int64{x, y}) || len(m.ExplicitAssetIDs) != 0 || !slices.Equal(m.EvidenceAssetIDs, []int64{y}) {
		t.Fatalf("mixed task scope with evidence or inherited hidden evidence: %+v", m)
	}
	if _, err := d.Exec("UPDATE assets SET company_id=$1 WHERE id=$2", b, y); err != nil {
		t.Fatal(err)
	}
	p, err = d.GetTaskProposal(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Assets) != 1 || len(p.AssetIDs) != 2 {
		t.Fatalf("moved asset: %+v", p)
	}
	if _, err := CheckProposalOverlap(ctx, d, in, allowed, p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("moved asset overlap: %v", err)
	}
}

// The review receipt covers matches outside the visible window as well.
func TestTaskProposalReviewIncludesTruncatedMatches(t *testing.T) {
	d, _, cs := testSetup(t)
	defer d.Close()
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	co, _, err := cs.UpsertCompany(fmt.Sprintf("review-cap-%d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cs.DeleteCompanyWithAssets(co, true)
	x := seedTreeAsset(t, d, "root_domain", map[string]any{"company_id": co, "domain": fmt.Sprintf("cap-%d.invalid", stamp)})
	_, err = d.Exec(`INSERT INTO task_proposals(company_id,token_id,request_id,request_hash,goal,asset_ids)
        SELECT $1,$2,k::text,'test','check metadata',ARRAY[$3::bigint] FROM generate_series(1,101) k`, co, stamp, x)
	if err != nil {
		t.Fatal(err)
	}
	current := func() *ProposalOverlap {
		result, err := CheckProposalOverlap(ctx, d, ProposalInput{co, "check metadata", []int64{x}}, []int64{co}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	prior := current()
	if len(prior.Matches) != 100 || !prior.Truncated {
		t.Fatalf("unexpected window: matches=%d truncated=%v", len(prior.Matches), prior.Truncated)
	}
	_, err = d.Exec("UPDATE task_proposals SET goal='updated' WHERE company_id=$1 AND id=(SELECT min(id) FROM task_proposals WHERE company_id=$1)", co)
	if err != nil {
		t.Fatal(err)
	}
	next := current()
	if next.ReviewKey == prior.ReviewKey || len(next.Matches) != 100 || !next.Truncated {
		t.Fatal("review omitted changed match outside display window")
	}
	for i := range next.Matches {
		if next.Matches[i].ID != prior.Matches[i].ID || next.Matches[i].Goal != prior.Matches[i].Goal {
			t.Fatal("visible window unexpectedly changed")
		}
	}
}

func TestTaskProposalTextOnlyNeverClaimsAssets(t *testing.T) {
	d, _, cs := testSetup(t)
	defer d.Close()
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	co, _, err := cs.UpsertCompany(fmt.Sprintf("text-only-%d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cs.DeleteCompanyWithAssets(co, true)
	asset := func(prefix string) int64 {
		return seedTreeAsset(t, d, "root_domain", map[string]any{"company_id": co, "domain": fmt.Sprintf("%s-%d.invalid", prefix, stamp)})
	}
	x, y := asset("selected"), asset("distinct")
	goal := "Review Contacts"
	submit := func(key, goal string) *TaskProposal {
		p, err := d.SubmitTaskProposal(ctx, stamp, key, ProposalInput{co, goal, []int64{y}}, []int64{co})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	draft := submit("draft", "REVIEW\tcontacts")
	submit("unrelated", "Review domain labels")
	expected := map[int64]string{}
	for _, field := range []string{"title", "description"} {
		p := submit(field, "older objective")
		task, err := d.CreateTaskWithOptions("metadata audit", p.Goal, TaskCreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer d.DeleteTask(task.ID)
		var name, description string
		if field == "title" {
			name = goal
		} else {
			description = goal
		}
		if _, err = d.Exec(`UPDATE tasks SET name=$2,description=$3 WHERE id=$1`, task.ID, name, description); err != nil {
			t.Fatal(err)
		}
		if _, err = d.Exec(`UPDATE task_proposals SET status='confirmed',task_id=$2 WHERE id=$1`, p.ID, task.ID); err != nil {
			t.Fatal(err)
		}
		expected[task.ID] = field
	}
	in := ProposalInput{co, goal, []int64{x}}
	got, err := CheckProposalOverlap(ctx, d, in, []int64{co}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.UnmatchedAssetIDs, []int64{x}) || len(got.Matches) != 3 {
		t.Fatalf("text overlap: %+v", got)
	}
	for _, m := range got.Matches {
		if m.AssetIDs == nil || m.ExplicitAssetIDs == nil || m.EvidenceAssetIDs == nil || len(m.AssetIDs)+len(m.ExplicitAssetIDs)+len(m.EvidenceAssetIDs) != 0 {
			t.Fatalf("text match claimed assets: %+v", m)
		}
		if m.Kind == "draft" {
			if m.ID != draft.ID || m.TextMatch != "goal" || !m.ExactGoal {
				t.Fatalf("draft match: %+v", m)
			}
		} else if expected[m.ID] != m.TextMatch || m.ExactGoal {
			t.Fatalf("task match: %+v", m)
		}
	}
}
