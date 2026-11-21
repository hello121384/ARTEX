package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type AutoDispatchResult struct {
	Status            string
	Task              *Task
	TaskID            int64
	AssetIDs          []int64
	DuplicateAssetIDs []int64
	DuplicateTaskIDs  []int64
	DuplicateDraftIDs []int64
	AlreadyProcessed  bool
}

func dispatchAssetsToSkip(matches []ProposalMatch) (map[int64]bool, map[int64]bool, map[int64]bool) {
	assets := map[int64]bool{}
	tasks := map[int64]bool{}
	drafts := map[int64]bool{}
	for _, match := range matches {
		if !match.ExactGoal {
			continue
		}
		var duplicateIDs []int64
		switch match.Kind {
		case "draft":
			if match.Status == "pending" {
				duplicateIDs = match.AssetIDs
				if len(duplicateIDs) > 0 {
					drafts[match.ID] = true
				}
			}
		case "task":
			switch match.Status {
			case "created", "queued", "running", "paused":
				duplicateIDs = match.AssetIDs
			default:
				duplicateIDs = match.EvidenceAssetIDs
			}
			if len(duplicateIDs) > 0 {
				tasks[match.ID] = true
			}
		}
		for _, id := range duplicateIDs {
			assets[id] = true
		}
	}
	return assets, tasks, drafts
}

// AutoDispatchTaskProposal deduplicates and creates a queued task in one
// transaction. Only the selected, still company-owned assets are attached.
func (d *DB) AutoDispatchTaskProposal(ctx context.Context, tokenID int64, requestID string, input ProposalInput, allowed []int64) (*AutoDispatchResult, error) {
	input, err := normalizeProposal(input, allowed)
	if err != nil {
		return nil, err
	}
	requestID = strings.TrimSpace(requestID)
	if tokenID <= 0 || requestID == "" || len(requestID) > 128 {
		return nil, fmt.Errorf("%w: invalid request_id", ErrProposalInvalid)
	}
	requestHash, err := proposalInputHash(input)
	if err != nil {
		return nil, err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockCompanyScopeMutation(tx); err != nil {
		return nil, err
	}
	if err := LockProposalCompany(ctx, tx, input.CompanyID); err != nil {
		return nil, err
	}
	if err := validateProposalAssets(ctx, tx, input, true); err != nil {
		return nil, err
	}
	var proposalID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO task_proposals(company_id,token_id,request_id,request_hash,goal,asset_ids)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(token_id,request_id) DO UPDATE SET request_id=EXCLUDED.request_id
		WHERE task_proposals.request_hash=EXCLUDED.request_hash RETURNING id`,
		input.CompanyID, tokenID, requestID, requestHash, input.Goal, input.AssetIDs).Scan(&proposalID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: request_id was used for different input", ErrProposalConflict)
	}
	if err != nil {
		return nil, err
	}
	p, err := LoadTaskProposal(ctx, tx, proposalID, true)
	if err != nil {
		return nil, err
	}
	result := &AutoDispatchResult{AssetIDs: slices.Clone(p.AssetIDs), DuplicateAssetIDs: slices.Clone(p.DuplicateAssetIDs), DuplicateTaskIDs: slices.Clone(p.DuplicateTaskIDs), DuplicateDraftIDs: slices.Clone(p.DuplicateDraftIDs)}
	if p.Status == "confirmed" {
		result.Status, result.TaskID, result.AlreadyProcessed = "dispatched", p.TaskID, true
		return result, tx.Commit()
	}
	if p.Status == "deduplicated" {
		result.Status, result.AlreadyProcessed = "deduplicated", true
		return result, tx.Commit()
	}
	if p.Status != "pending" {
		return nil, ErrProposalConflict
	}
	allMatches, err := checkProposalOverlap(ctx, tx, p.ProposalInput, nil, p.ID, true)
	if err != nil {
		return nil, err
	}
	visibleMatches, err := checkProposalOverlap(ctx, tx, p.ProposalInput, allowed, p.ID, true)
	if err != nil {
		return nil, err
	}
	skipAssets, _, _ := dispatchAssetsToSkip(allMatches.allMatches)
	_, visibleTaskMatches, visibleDraftMatches := dispatchAssetsToSkip(visibleMatches.allMatches)
	for id := range visibleTaskMatches {
		result.DuplicateTaskIDs = append(result.DuplicateTaskIDs, id)
	}
	for id := range visibleDraftMatches {
		result.DuplicateDraftIDs = append(result.DuplicateDraftIDs, id)
	}
	slices.Sort(result.DuplicateTaskIDs)
	slices.Sort(result.DuplicateDraftIDs)
	remaining := make([]int64, 0, len(p.AssetIDs))
	for _, id := range p.AssetIDs {
		if skipAssets[id] {
			result.DuplicateAssetIDs = append(result.DuplicateAssetIDs, id)
		} else {
			remaining = append(remaining, id)
		}
	}
	result.DuplicateAssetIDs = sortedUnique(result.DuplicateAssetIDs)
	if len(remaining) == 0 {
		_, err := tx.ExecContext(ctx, `UPDATE task_proposals SET status='deduplicated',
			duplicate_asset_ids=$2,duplicate_task_ids=$3,duplicate_draft_ids=$4,version=version+1 WHERE id=$1`,
			p.ID, result.DuplicateAssetIDs, result.DuplicateTaskIDs, result.DuplicateDraftIDs)
		if err != nil {
			return nil, err
		}
		result.Status = "deduplicated"
		result.AssetIDs = []int64{}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	_, err = tx.ExecContext(ctx, "UPDATE task_proposals SET asset_ids=$2,duplicate_asset_ids=$3,duplicate_task_ids=$4,duplicate_draft_ids=$5,version=version+1 WHERE id=$1", p.ID, remaining, result.DuplicateAssetIDs, result.DuplicateTaskIDs, result.DuplicateDraftIDs)
	if err != nil {
		return nil, err
	}
	var labels []string
	for _, asset := range p.Assets {
		if slices.Contains(remaining, asset.ID) {
			labels = append(labels, fmt.Sprintf("%s (#%d)", asset.Label, asset.ID))
		}
	}
	description := fmt.Sprintf("MCP 自动派发；授权企业 %s；本任务仅关联资产：%s", p.CompanyName, strings.Join(labels, "、"))
	opts := TaskCreateOptions{Remark: fmt.Sprintf("MCP 自动派发 request_id=%s", requestID)}
	task, err := createTaskTx(tx, description, p.Goal, opts)
	if err != nil {
		return nil, err
	}
	update, err := tx.ExecContext(ctx, `UPDATE assets SET task_ids=CASE
			WHEN $1=ANY(task_ids) THEN task_ids ELSE array_append(task_ids,$1) END
		WHERE company_id=$2 AND id=ANY($3::bigint[])`, task.ID, input.CompanyID, remaining)
	if err != nil {
		return nil, err
	}
	n, err := update.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != int64(len(remaining)) {
		return nil, sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, "UPDATE task_asset_links SET source='manual',source_summary=$2 WHERE task_id=$1", task.ID, opts.Remark); err != nil {
		return nil, err
	}
	queuedAt := time.Now().UTC()
	if err := tx.QueryRowContext(ctx, `UPDATE tasks SET queued=true,queued_at=$2,queue_mode='bootstrap' WHERE id=$1
		RETURNING queued,queued_at,queue_mode`, task.ID, queuedAt).Scan(&task.Queued, &task.QueuedAt, &task.QueueMode); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE task_proposals SET status='confirmed',task_id=$2,version=version+1 WHERE id=$1", p.ID, task.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	task.CompanyIDs = []int64{input.CompanyID}
	result.Status, result.Task, result.TaskID, result.AssetIDs = "dispatched", task, task.ID, remaining
	return result, nil
}

// sortedUnique returns a stable, deduplicated list for persistent MCP results.
func sortedUnique(ids []int64) []int64 {
	if len(ids) == 0 {
		return []int64{}
	}
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
