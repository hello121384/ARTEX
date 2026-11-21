package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ConfirmTaskProposal creates the reviewed task, attaches exactly the reviewed
// assets and consumes the proposal in one transaction. Only a newly committed
// task is returned to the operator's caller for activation.
func (d *DB) ConfirmTaskProposal(ctx context.Context, id, version int64, reviewKey string) (created *Task, taskID int64, err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var companyID int64
	if err := tx.QueryRowContext(ctx, `SELECT company_id FROM task_proposals WHERE id=$1`, id).Scan(&companyID); err != nil {
		return nil, 0, err
	}
	if err := lockCompanyScopeMutation(tx); err != nil {
		return nil, 0, err
	}
	if err := LockProposalCompany(ctx, tx, companyID); err != nil {
		return nil, 0, err
	}
	p, err := LoadTaskProposal(ctx, tx, id, true)
	if err != nil {
		return nil, 0, err
	}
	if p.Status == "confirmed" && p.TaskID > 0 {
		return nil, p.TaskID, nil
	}
	if p.Status != "pending" || version != p.Version || reviewKey == "" {
		return nil, 0, ErrProposalConflict
	}
	in := ProposalInput{CompanyID: p.CompanyID, Goal: p.Goal, AssetIDs: p.AssetIDs}
	overlap, err := CheckProposalOverlap(ctx, tx, in, nil, id)
	if err != nil {
		return nil, 0, err
	}
	if overlap.ReviewKey != reviewKey {
		return nil, 0, ErrProposalConflict
	}
	var labels []string
	for _, a := range p.Assets {
		labels = append(labels, fmt.Sprintf("%s (#%d)", a.Label, a.ID))
	}
	description := fmt.Sprintf("草稿 #%d 确认的资产：%s", id, strings.Join(labels, "、"))
	opts := TaskCreateOptions{Remark: fmt.Sprintf("由人工确认草稿 #%d 创建", id)}
	if err := NormalizeNoResultOptions(&opts); err != nil {
		return nil, 0, err
	}
	task, err := createTaskTx(tx, description, p.Goal, opts)
	if err != nil {
		return nil, 0, err
	}
	// The asset update fires the normal task_asset_links trigger. Never seed
	// a company scope here: that would attach every asset in the enterprise.
	result, err := tx.ExecContext(ctx, `UPDATE assets SET task_ids=array_append(task_ids,$1)
		WHERE company_id=$2 AND id=ANY($3::bigint[])`, task.ID, companyID, p.AssetIDs)
	if err != nil {
		return nil, 0, err
	}
	if n, err := result.RowsAffected(); err != nil || n != int64(len(p.AssetIDs)) {
		if err != nil {
			return nil, 0, err
		}
		return nil, 0, sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_asset_links SET source='manual',source_summary=$2 WHERE task_id=$1`, task.ID, opts.Remark); err != nil {
		return nil, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_proposals SET status='confirmed',task_id=$2,version=version+1 WHERE id=$1`, id, task.ID); err != nil {
		return nil, 0, err
	}
	// Persist the initial queue state with the approval so a restart between
	// commit and admission can recover the reviewed task from the normal FIFO.
	if err := tx.QueryRowContext(ctx, `UPDATE tasks SET queued=true, queued_at=now(), queue_mode='bootstrap'
		WHERE id=$1 RETURNING queued, queued_at, queue_mode`, task.ID).Scan(&task.Queued, &task.QueuedAt, &task.QueueMode); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	task.CompanyIDs = []int64{companyID}
	return task, task.ID, nil
}
