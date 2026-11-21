package db

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

// This exercises task creation and retry semantics in PostgreSQL without
// running a task engine or contacting any asset.
func TestConfirmTaskProposalAtomicAndScoped(t *testing.T) {
	d, store, companies := testSetup(t)
	defer d.Close()
	ctx := t.Context()
	name := fmt.Sprintf("proposal-confirm-%d", time.Now().UnixNano())
	company, _, err := companies.UpsertCompany(name, "")
	if err != nil {
		t.Fatal(err)
	}
	defer companies.DeleteCompanyWithAssets(company, true)
	var ids []int64
	for _, suffix := range []string{"a", "b", "c"} {
		ids = append(ids, seedTreeAsset(t, d, "root_domain", map[string]any{"domain": name + "-" + suffix + ".invalid", "company_id": company}))
	}
	allowed := []int64{company}
	in := ProposalInput{CompanyID: company, Goal: "核对归属", AssetIDs: ids[:2]}
	first, err := d.SubmitTaskProposal(ctx, 10, name+"-first", in, allowed)
	if err != nil {
		t.Fatal(err)
	}
	old, err := CheckProposalOverlap(ctx, d, in, allowed, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.SubmitTaskProposal(ctx, 10, name+"-second", ProposalInput{CompanyID: company, Goal: in.Goal, AssetIDs: ids[1:]}, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.ConfirmTaskProposal(ctx, first.ID, first.Version, old.ReviewKey); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("stale review accepted: %v", err)
	}
	review, err := CheckProposalOverlap(ctx, d, in, allowed, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Matches) != 1 || review.Matches[0].ID != second.ID || !slices.Equal(review.Matches[0].AssetIDs, ids[1:2]) || !slices.Equal(review.UnmatchedAssetIDs, ids[:1]) {
		t.Fatalf("expected partial overlap: %+v", review)
	}
	p, err := d.EditTaskProposal(ctx, first.ID, first.Version, ProposalInput{CompanyID: company, Goal: in.Goal, AssetIDs: ids[:1]})
	if err != nil {
		t.Fatal(err)
	}
	review, err = CheckProposalOverlap(ctx, d, p.ProposalInput, allowed, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	type confirmation struct {
		task *Task
		id   int64
		err  error
	}
	results := make(chan confirmation, 2)
	for range 2 {
		go func() {
			task, id, err := d.ConfirmTaskProposal(ctx, p.ID, p.Version, review.ReviewKey)
			results <- confirmation{task, id, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.id <= 0 || a.id != b.id || (a.task == nil) == (b.task == nil) {
		t.Fatalf("expected one new task and one retry: %+v %+v", a, b)
	}
	defer d.DeleteTask(a.id)
	rows, err := store.QueryByTask(a.id, "", 10, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != ids[0] {
		t.Fatalf("unexpected attached assets: %+v %v", rows, err)
	}
	task, err := d.GetTask(a.id)
	if err != nil || !slices.Equal(task.CompanyIDs, allowed) {
		t.Fatalf("single task lost company membership: %+v %v", task, err)
	}
	tasks, err := d.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(tasks, func(t *Task) bool { return t.ID == a.id && slices.Equal(t.CompanyIDs, allowed) }) {
		t.Fatal("task list lost company membership")
	}
	page, err := CheckProposalOverlap(ctx, d, p.ProposalInput, allowed, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := slices.ContainsFunc(page.Matches, func(m ProposalMatch) bool {
		return m.Kind == "task" && m.ID == a.id && slices.Equal(m.ExplicitAssetIDs, ids[:1])
	})
	if !found {
		t.Fatalf("confirmed work missing from history: %+v", page)
	}
}
