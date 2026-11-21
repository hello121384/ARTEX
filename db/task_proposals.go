package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrProposalInvalid  = errors.New("invalid task proposal")
	ErrProposalConflict = errors.New("task proposal changed")
)

const maxProposalAssets = 200

type ProposalInput struct {
	CompanyID int64   `json:"company_id"`
	Goal      string  `json:"goal"`
	AssetIDs  []int64 `json:"asset_ids"`
}

type ProposalAsset struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

type TaskProposal struct {
	ProposalInput
	ID                int64           `json:"id"`
	CompanyName       string          `json:"company_name"`
	Assets            []ProposalAsset `json:"assets"`
	Status            string          `json:"status"`
	Version           int64           `json:"version"`
	TaskID            int64           `json:"task_id,omitempty"`
	DuplicateAssetIDs []int64         `json:"duplicate_asset_ids,omitempty"`
	DuplicateTaskIDs  []int64         `json:"duplicate_task_ids,omitempty"`
	DuplicateDraftIDs []int64         `json:"duplicate_draft_ids,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

type proposalQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func normalizeProposal(in ProposalInput, allowed []int64) (ProposalInput, error) {
	in.Goal = strings.TrimSpace(in.Goal)
	if in.CompanyID <= 0 || in.Goal == "" || utf8.RuneCountInString(in.Goal) > 4000 || len(in.AssetIDs) == 0 || len(in.AssetIDs) > maxProposalAssets {
		return in, fmt.Errorf("%w: require a company, goal (1–4000 characters), and 1–%d assets", ErrProposalInvalid, maxProposalAssets)
	}
	if len(allowed) > 0 && !slices.Contains(allowed, in.CompanyID) {
		return in, sql.ErrNoRows
	}
	in.AssetIDs = slices.Clone(in.AssetIDs)
	slices.Sort(in.AssetIDs)
	if in.AssetIDs[0] <= 0 {
		return in, fmt.Errorf("%w: invalid asset id", ErrProposalInvalid)
	}
	in.AssetIDs = slices.Compact(in.AssetIDs)
	return in, nil
}

// LockProposalCompany serializes draft edits and human acceptance for a company.
func LockProposalCompany(ctx context.Context, tx *sql.Tx, companyID int64) error {
	var id int64
	return tx.QueryRowContext(ctx, `SELECT id FROM companies WHERE id=$1 FOR NO KEY UPDATE`, companyID).Scan(&id)
}

// A locking validation also prevents asset reassignment until the transaction commits.
func validateProposalAssets(ctx context.Context, q proposalQueryer, in ProposalInput, lock bool) error {
	query := `SELECT count(*) FROM (SELECT id FROM assets WHERE company_id=$1 AND id=ANY($2::bigint[]) ORDER BY id`
	if lock {
		query += " FOR SHARE"
	}
	var n int
	if err := q.QueryRowContext(ctx, query+") a", in.CompanyID, in.AssetIDs).Scan(&n); err != nil {
		return err
	}
	if n != len(in.AssetIDs) {
		return sql.ErrNoRows
	}
	return nil
}

const proposalColumns = `SELECT p.id,p.version,p.status,p.company_id,c.name,p.goal,array_to_json(p.asset_ids),COALESCE(p.task_id,0),array_to_json(p.duplicate_asset_ids),array_to_json(p.duplicate_task_ids),array_to_json(p.duplicate_draft_ids),p.created_at,
    COALESCE((SELECT json_agg(json_build_object('id',a.id,'type',a.type,'label',
        COALESCE(NULLIF(a.url,''),NULLIF(a.domain,''),NULLIF(a.ip,''),NULLIF(a.app_name,''),a.id::text)) ORDER BY a.id)
        FROM assets a WHERE a.id=ANY(p.asset_ids) AND a.company_id=p.company_id),'[]'::json)
    FROM task_proposals p JOIN companies c ON c.id=p.company_id`

func scanProposal(row interface{ Scan(...any) error }) (*TaskProposal, error) {
	var p TaskProposal
	var ids, assets, duplicateAssets, duplicateTasks, duplicateDrafts []byte
	if err := row.Scan(&p.ID, &p.Version, &p.Status, &p.CompanyID, &p.CompanyName, &p.Goal, &ids, &p.TaskID, &duplicateAssets, &duplicateTasks, &duplicateDrafts, &p.CreatedAt, &assets); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(ids, &p.AssetIDs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(assets, &p.Assets); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(duplicateAssets, &p.DuplicateAssetIDs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(duplicateTasks, &p.DuplicateTaskIDs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(duplicateDrafts, &p.DuplicateDraftIDs); err != nil {
		return nil, err
	}
	return &p, nil
}

func LoadTaskProposal(ctx context.Context, q proposalQueryer, id int64, forUpdate bool) (*TaskProposal, error) {
	query := proposalColumns + " WHERE p.id=$1"
	if forUpdate {
		query += " FOR UPDATE OF p"
	}
	return scanProposal(q.QueryRowContext(ctx, query, id))
}

func (d *DB) GetTaskProposal(ctx context.Context, id int64) (*TaskProposal, error) {
	return LoadTaskProposal(ctx, d, id, false)
}

func (d *DB) ListTaskProposals(ctx context.Context, limit, offset int) ([]TaskProposal, error) {
	if limit < 1 {
		limit = 25
	}
	if offset < 0 {
		return nil, ErrProposalInvalid
	}
	rows, err := d.QueryContext(ctx, proposalColumns+" WHERE p.status='pending' ORDER BY p.id DESC LIMIT $1 OFFSET $2", min(limit, 101), offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskProposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (d *DB) SubmitTaskProposal(ctx context.Context, tokenID int64, requestID string, in ProposalInput, allowed []int64) (*TaskProposal, error) {
	in, err := normalizeProposal(in, allowed)
	if err != nil {
		return nil, err
	}
	requestID = strings.TrimSpace(requestID)
	if tokenID <= 0 || requestID == "" || utf8.RuneCountInString(requestID) > 128 {
		return nil, fmt.Errorf("%w: invalid request_id", ErrProposalInvalid)
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := LockProposalCompany(ctx, tx, in.CompanyID); err != nil {
		return nil, err
	}
	if err := validateProposalAssets(ctx, tx, in, true); err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO task_proposals(company_id,token_id,request_id,request_hash,goal,asset_ids)
        VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(token_id,request_id) DO UPDATE SET request_id=EXCLUDED.request_id
        WHERE task_proposals.request_hash=EXCLUDED.request_hash RETURNING id`, in.CompanyID, tokenID, requestID, hash, in.Goal, in.AssetIDs).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: request_id was used for a different proposal", ErrProposalConflict)
	}
	if err != nil {
		return nil, err
	}
	p, err := LoadTaskProposal(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

// proposalInputHash signs the normalized input without including its token or request_id.
func proposalInputHash(in ProposalInput) (string, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// GetTaskProposalByRequest verifies idempotency before an automatic dispatcher spends quota.
func (d *DB) GetTaskProposalByRequest(ctx context.Context, tokenID int64, requestID string, in ProposalInput, allowed []int64) (*TaskProposal, bool, error) {
	in, err := normalizeProposal(in, allowed)
	if err != nil {
		return nil, false, err
	}
	requestID = strings.TrimSpace(requestID)
	if tokenID <= 0 || requestID == "" || utf8.RuneCountInString(requestID) > 128 {
		return nil, false, fmt.Errorf("%w: invalid request_id", ErrProposalInvalid)
	}
	hash, err := proposalInputHash(in)
	if err != nil {
		return nil, false, err
	}
	var id int64
	var storedHash string
	err = d.QueryRowContext(ctx, "SELECT id,request_hash FROM task_proposals WHERE token_id=$1 AND request_id=$2", tokenID, requestID).Scan(&id, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if storedHash != hash {
		return nil, false, fmt.Errorf("%w: request_id was used for different input", ErrProposalConflict)
	}
	p, err := d.GetTaskProposal(ctx, id)
	return p, err == nil, err
}

func (d *DB) EditTaskProposal(ctx context.Context, id, version int64, in ProposalInput) (*TaskProposal, error) {
	in, err := normalizeProposal(in, nil)
	if err != nil {
		return nil, err
	}
	return d.changeTaskProposal(ctx, id, version, &in)
}

func (d *DB) WithdrawTaskProposal(ctx context.Context, id, version int64) (*TaskProposal, error) {
	return d.changeTaskProposal(ctx, id, version, nil)
}

func (d *DB) changeTaskProposal(ctx context.Context, id, version int64, in *ProposalInput) (*TaskProposal, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var companyID int64
	if err := tx.QueryRowContext(ctx, "SELECT company_id FROM task_proposals WHERE id=$1", id).Scan(&companyID); err != nil {
		return nil, err
	}
	if err := LockProposalCompany(ctx, tx, companyID); err != nil {
		return nil, err
	}
	p, err := LoadTaskProposal(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if p.Version != version || p.Status != "pending" {
		return nil, ErrProposalConflict
	}
	if in == nil {
		_, err = tx.ExecContext(ctx, "UPDATE task_proposals SET version=version+1,status='withdrawn' WHERE id=$1", id)
	} else {
		if in.CompanyID != companyID {
			return nil, fmt.Errorf("%w: company cannot be changed", ErrProposalInvalid)
		}
		for _, id := range in.AssetIDs {
			if !slices.Contains(p.AssetIDs, id) {
				return nil, fmt.Errorf("%w: edits may only narrow the selected assets", ErrProposalInvalid)
			}
		}
		if err := validateProposalAssets(ctx, tx, *in, true); err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE task_proposals SET version=version+1,goal=$2,asset_ids=$3 WHERE id=$1", id, in.Goal, in.AssetIDs)
	}
	if err != nil {
		return nil, err
	}
	p, err = LoadTaskProposal(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit()
}
