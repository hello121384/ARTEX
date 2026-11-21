package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// ExplicitAssetIDs identifies assets explicitly selected in a proposal.
// A matching scope, or even a fact anchor, does not assert the goal was met.
type ProposalMatch struct {
	Kind             string    `json:"kind"`
	ID               int64     `json:"id"`
	Goal             string    `json:"goal"`
	Status           string    `json:"status"`
	TextMatch        string    `json:"text_match,omitempty"`
	ExactGoal        bool      `json:"exact_goal"`
	AssetIDs         []int64   `json:"asset_ids"`
	ExplicitAssetIDs []int64   `json:"explicit_asset_ids"`
	EvidenceAssetIDs []int64   `json:"evidence_asset_ids"`
	CreatedAt        time.Time `json:"created_at"`
}

type ProposalOverlap struct {
	Matches           []ProposalMatch `json:"matches"`
	UnmatchedAssetIDs []int64         `json:"unmatched_asset_ids"`
	ReviewKey         string          `json:"review_key"`
	Truncated         bool            `json:"truncated"`
	allMatches        []ProposalMatch
}

// Only local facts enter this query. Never join a related task's context into
// a visible task: that task may itself be outside the requesting token's scope.
const proposalOverlapQuery = `WITH selected AS (
    SELECT id,type,company_id,domain,root_domain,ip,icp,app_icp
    FROM assets WHERE company_id=$1 AND id=ANY($2::bigint[])
), affiliations AS (
    SELECT task_id,company_id FROM task_scope WHERE kind='company' AND company_id IS NOT NULL
    UNION ALL
    SELECT task_id,company_id FROM task_proposals WHERE status='confirmed'
), visible AS (
    SELECT t.id,t.exploration_id,t.goal,t.name,t.description,t.created_at,
           CASE WHEN t.paused THEN 'paused' WHEN t.queued THEN 'queued' ELSE t.status END AS status
    FROM tasks t WHERE t.deleted_at IS NULL
      AND (EXISTS (SELECT 1 FROM affiliations c WHERE c.task_id=t.id AND c.company_id=$1)
        OR (cardinality($3::bigint[])=0 AND NOT EXISTS (SELECT 1 FROM affiliations c WHERE c.task_id=t.id)))
      AND (cardinality($3::bigint[])=0 OR NOT EXISTS (SELECT 1 FROM affiliations c WHERE c.task_id=t.id AND NOT(c.company_id=ANY($3))))
), task_hits AS (
    SELECT l.task_id,a.id AS asset_id,false AS explicit,false AS evidence
      FROM selected a JOIN task_asset_links l ON l.asset_id=a.id
    UNION ALL
    SELECT n.task_id,a.id,false,false FROM selected a
      JOIN task_scope n ON ((n.kind='company' AND a.company_id=n.company_id)
        OR (n.kind='root_domain' AND (a.root_domain=n.domain OR (a.type='root_domain' AND a.domain=n.domain)))
        OR (n.kind='subdomain' AND a.domain=n.domain)
        OR (n.kind IN ('cidr','ip') AND n.net >>= try_inet(a.ip))
        OR (n.kind='icp' AND (lower(regexp_replace(COALESCE(a.icp,''),'[[:space:]]+','','g'))=n.value
                            OR lower(regexp_replace(COALESCE(a.app_icp,''),'[[:space:]]+','','g'))=n.value)))
    UNION ALL
    SELECT v.id,a.id,false,n.kind='fact' AND n.state='confirmed'
      FROM selected a JOIN exploration_anchors e ON e.asset_id=a.id
      JOIN exploration_nodes n ON n.id=e.node_id JOIN visible v ON v.exploration_id=n.exploration_id
    UNION ALL
    SELECT p.task_id,a.id,true,false FROM selected a JOIN task_proposals p ON p.status='confirmed'
       AND p.company_id=$1 AND a.id=ANY(p.asset_ids)
), drafts AS (
    SELECT id,goal,status,created_at,asset_ids FROM task_proposals
    WHERE company_id=$1 AND status='pending' AND id<>$4
), asset_hits AS (
    SELECT 'task'::text AS kind,task_id AS id,asset_id,
           bool_or(explicit) AS explicit,bool_or(evidence) AS evidence
      FROM task_hits GROUP BY task_id,asset_id
    UNION ALL
    SELECT 'draft',p.id,a.id,true,false FROM drafts p JOIN selected a ON a.id=ANY(p.asset_ids)
), candidates AS (
    SELECT 'task'::text AS kind,v.id,v.goal,v.status,v.created_at,
        CASE WHEN lower(trim(regexp_replace(v.goal,'[[:space:]]+',' ','g')))=$5 THEN 'goal'
             WHEN lower(trim(regexp_replace(v.name,'[[:space:]]+',' ','g')))=$5 THEN 'title'
             WHEN lower(trim(regexp_replace(v.description,'[[:space:]]+',' ','g')))=$5 THEN 'description'
             ELSE '' END AS text_match,
        EXISTS (SELECT 1 FROM affiliations c WHERE c.task_id=v.id AND c.company_id=$1) AS same_company
      FROM visible v
    UNION ALL
    SELECT 'draft',p.id,p.goal,p.status,p.created_at,
        CASE WHEN lower(trim(regexp_replace(p.goal,'[[:space:]]+',' ','g')))=$5 THEN 'goal' ELSE '' END,true
      FROM drafts p
)
SELECT c.kind,c.id,c.goal,c.status,c.created_at,c.text_match,
    COALESCE(json_agg(h.asset_id ORDER BY h.asset_id) FILTER (WHERE h.asset_id IS NOT NULL),'[]'::json),
    COALESCE(json_agg(h.asset_id ORDER BY h.asset_id) FILTER (WHERE h.explicit),'[]'::json),
    COALESCE(json_agg(h.asset_id ORDER BY h.asset_id) FILTER (WHERE h.evidence),'[]'::json)
FROM candidates c LEFT JOIN asset_hits h ON h.kind=c.kind AND h.id=c.id
WHERE h.asset_id IS NOT NULL OR (c.same_company AND c.text_match<>'')
GROUP BY c.kind,c.id,c.goal,c.status,c.created_at,c.text_match
ORDER BY (count(h.asset_id)>0) DESC, (c.text_match='goal') DESC,
    COALESCE(bool_or(h.explicit),false) DESC, (c.status IN ('pending','queued','running','created','paused')) DESC,
    c.created_at DESC,c.kind,c.id DESC`

func normalizedGoal(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func CheckProposalOverlap(ctx context.Context, q proposalQueryer, in ProposalInput, allowed []int64, excludeDraftID int64) (*ProposalOverlap, error) {
	return checkProposalOverlap(ctx, q, in, allowed, excludeDraftID, false)
}

func checkProposalOverlap(ctx context.Context, q proposalQueryer, in ProposalInput, allowed []int64, excludeDraftID int64, retainAll bool) (*ProposalOverlap, error) {
	in, err := normalizeProposal(in, allowed)
	if err != nil {
		return nil, err
	}
	_, inTx := q.(*sql.Tx)
	if err := validateProposalAssets(ctx, q, in, inTx); err != nil {
		return nil, err
	}
	if allowed == nil {
		allowed = []int64{}
	}
	rows, err := q.QueryContext(ctx, proposalOverlapQuery, in.CompanyID, in.AssetIDs, allowed, excludeDraftID, normalizedGoal(in.Goal))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &ProposalOverlap{Matches: []ProposalMatch{}, UnmatchedAssetIDs: []int64{}}
	matched := map[int64]bool{}
	sum := sha256.New()
	enc := json.NewEncoder(sum)
	if err := enc.Encode(in); err != nil {
		return nil, err
	}
	if err := enc.Encode(excludeDraftID); err != nil {
		return nil, err
	}
	for rows.Next() {
		var m ProposalMatch
		var hits, explicit, evidence []byte
		if err := rows.Scan(&m.Kind, &m.ID, &m.Goal, &m.Status, &m.CreatedAt, &m.TextMatch, &hits, &explicit, &evidence); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(hits, &m.AssetIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(explicit, &m.ExplicitAssetIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(evidence, &m.EvidenceAssetIDs); err != nil {
			return nil, err
		}
		m.ExactGoal = normalizedGoal(in.Goal) == normalizedGoal(m.Goal)
		for _, id := range m.AssetIDs {
			matched[id] = true
		}
		m.CreatedAt = m.CreatedAt.UTC()
		if err := enc.Encode(m); err != nil {
			return nil, err
		}
		if retainAll {
			result.allMatches = append(result.allMatches, m)
		}
		if len(result.Matches) < 100 {
			result.Matches = append(result.Matches, m)
		} else {
			result.Truncated = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range in.AssetIDs {
		if !matched[id] {
			result.UnmatchedAssetIDs = append(result.UnmatchedAssetIDs, id)
		}
	}
	result.ReviewKey = hex.EncodeToString(sum.Sum(nil))
	return result, nil
}
