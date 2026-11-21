package db

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

type CompanyGraphNode struct {
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	Expandable bool   `json:"expandable"`
}

type CompanyGraphPage struct {
	Nodes   []CompanyGraphNode `json:"nodes"`
	HasMore bool               `json:"has_more"`
}

var ErrInvalidGraphParent = errors.New("invalid company graph parent")

// A root represented only by a company's DNS records remains navigable even
// when the real root asset is unowned or belongs to a different company.
// The expression must match idx_av2_graph_company_root_name in schema.sql.
const companyGraphRoot = `CASE WHEN a.type='root_domain' THEN a.domain ELSE a.root_domain END`

// Use an index seek to the next distinct name. A company with one root and a
// million DNS records needs two seeks, not a DISTINCT over those million rows.
const companyGraphRootsPage = `
roots(domain, position) AS (
  (SELECT ` + companyGraphRoot + `, 1::bigint FROM assets a
   WHERE COALESCE(a.company_id,0)=$1 AND a.type IN ('root_domain','subdomain')
     AND (` + companyGraphRoot + `) > ''
   ORDER BY ` + companyGraphRoot + ` LIMIT 1)
  UNION ALL
  SELECT next_root.domain, roots.position+1 FROM roots
  CROSS JOIN LATERAL (
    SELECT ` + companyGraphRoot + ` AS domain FROM assets a
    WHERE COALESCE(a.company_id,0)=$1 AND a.type IN ('root_domain','subdomain')
      AND (` + companyGraphRoot + `) > roots.domain
    ORDER BY ` + companyGraphRoot + ` LIMIT 1
  ) next_root
  WHERE roots.position - $2::bigint < $3::bigint
),
page AS (
  SELECT COALESCE(r.id,0) AS id, 'root_domain' AS type, roots.domain,
         roots.domain AS root_domain, '' AS ip, '' AS url, 0 AS port,
         '' AS service_type, '' AS app_name, roots.position AS position
  FROM roots LEFT JOIN LATERAL (
    SELECT id FROM assets WHERE type='root_domain' AND domain=roots.domain
      AND COALESCE(company_id,0)=$1 LIMIT 1
  ) r ON true
  WHERE roots.position > $3
)`

// Normal writes persist the URL host in domain. Imported URL-only assets also
// participate; do not confuse a DNS host's resolved IP with its URL host.
const companyGraphHosts = `graph_assets AS NOT MATERIALIZED (
  SELECT a.*, asset_graph_host(a.domain,a.url,a.ip) AS graph_host,
    COALESCE(a.port, substring(a.url from '^[^:]+://(?:[^/@]+@)?(?:\[[^]]+\]|[^:/?#]+):([0-9]{1,5})(?:[/?#]|$)')::int,
      CASE lower(split_part(a.url,':',1)) WHEN 'https' THEN 443 WHEN 'http' THEN 80 ELSE 0 END) AS graph_port
  FROM assets a WHERE (a.company_id=$1 OR ($1=0 AND a.company_id IS NULL))
)`

// Compute a candidate's parent only after filtering by this branch's host or
// root. Scope all lookups to the same company, including fallback connectors.
const companyGraphParent = `CASE
  WHEN a.type='subdomain' THEN COALESCE('r:' || NULLIF(a.root_domain,''),'company')
  WHEN a.type IN ('service','endpoint') THEN COALESCE(
    CASE WHEN a.type='endpoint' THEN (
      SELECT 'a:' || s.id FROM graph_assets s
      WHERE s.type='service' AND s.graph_host=a.graph_host
      ORDER BY (s.graph_port=a.graph_port) DESC, s.id LIMIT 1
    ) END,
    (SELECT 'a:' || d.id FROM graph_assets d WHERE d.type='subdomain' AND d.domain=a.graph_host ORDER BY d.id LIMIT 1),
    (SELECT 'a:' || i.id FROM graph_assets i WHERE i.type='ip' AND i.ip IN (a.graph_host,a.ip)
     ORDER BY (i.ip=a.graph_host) DESC NULLS LAST, i.id LIMIT 1),
    (SELECT 'r:' || a.root_domain WHERE a.root_domain <> '' AND EXISTS (
       SELECT 1 FROM assets r WHERE COALESCE(r.company_id,0)=$1 AND r.type IN ('root_domain','subdomain')
         AND (CASE WHEN r.type='root_domain' THEN r.domain ELSE r.root_domain END)=a.root_domain
    )),
    'company')
  ELSE 'company' END`

// The same predicate supplies both the child page and its expand affordances.
// p is the requested parent (or a returned node); a is a candidate child.
const companyGraphIsChild = `(
  (p.type='company' AND a.type<>'root_domain')
  OR (p.type='root_domain' AND a.root_domain=p.domain AND a.type IN ('subdomain','service','endpoint'))
  OR (p.type='subdomain' AND a.graph_host=p.domain AND a.type IN ('service','endpoint'))
  OR (p.type='ip' AND p.ip IN (a.graph_host,a.ip) AND a.type IN ('service','endpoint'))
  OR (p.type='service' AND a.graph_host=p.graph_host AND a.type='endpoint')
) AND (` + companyGraphParent + `) =
  CASE p.type WHEN 'company' THEN 'company' WHEN 'root_domain' THEN 'r:'||p.domain ELSE 'a:'||p.id END`

const companyGraphChildrenPage = `
page AS (
  SELECT a.id,a.type,a.domain,a.root_domain,a.ip,a.url,a.port,a.service_type,a.app_name,
         a.graph_host, a.id AS position
  FROM (SELECT $4::text AS type,$5::bigint AS id,$6::text AS domain,$7::text AS ip,$8::text AS graph_host) p
  JOIN graph_assets a ON ` + companyGraphIsChild + `
  ORDER BY a.id LIMIT $2 OFFSET $3
)`

// ListCompanyGraphChildren reads a single page and never materializes a full
// asset graph. Empty parent lists roots; "company" lists other direct assets.
func (s *AssetStore) ListCompanyGraphChildren(ctx context.Context, companyID int64, parent string, offset, limit int) (*CompanyGraphPage, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	args := []any{companyID, limit + 1, offset}
	pageQuery := companyGraphRootsPage
	expandable := "true" // Root availability is resolved on click, without reading deeper assets on open.
	if parent != "" {
		pageQuery = companyGraphChildrenPage
		expandable = "EXISTS (SELECT 1 FROM graph_assets a WHERE " + companyGraphIsChild + ")"
	}

	var kind, domain, ip, host string
	var id int64
	switch {
	case parent == "" || parent == "company":
		kind = "company"
		if companyID != 0 {
			if err := s.db.QueryRowContext(ctx, "SELECT id FROM companies WHERE id=$1", companyID).Scan(&id); err != nil {
				return nil, err
			}
		}
	case strings.HasPrefix(parent, "a:"):
		var err error
		id, err = strconv.ParseInt(parent[2:], 10, 64)
		if err != nil || id <= 0 {
			return nil, ErrInvalidGraphParent
		}
		err = s.db.QueryRowContext(ctx, "WITH "+companyGraphHosts+`
SELECT type,COALESCE(domain,''),COALESCE(ip,''),COALESCE(graph_host,'') FROM graph_assets WHERE id=$2`, companyID, id).Scan(&kind, &domain, &ip, &host)
		if err != nil {
			return nil, err
		}
	case strings.HasPrefix(parent, "r:") && len(parent) > 2:
		domain, kind = parent[2:], "root_domain"
		var exists bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM assets WHERE (company_id=$1 OR ($1=0 AND company_id IS NULL)) AND type='subdomain' AND root_domain=$2
)`, companyID, domain).Scan(&exists)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, sql.ErrNoRows
		}
	default:
		return nil, ErrInvalidGraphParent
	}
	if parent != "" {
		args = append(args, kind, id, domain, ip, host)
	}
	query := "WITH RECURSIVE " + companyGraphHosts + "," + pageQuery + `
SELECT p.id,p.type,COALESCE(p.domain,''),COALESCE(p.root_domain,''),COALESCE(p.ip,''),
       COALESCE(p.url,''),COALESCE(p.port,0),COALESCE(p.service_type,''),COALESCE(p.app_name,''),
       ` + expandable + ` AS expandable
FROM page p ORDER BY p.position`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &CompanyGraphPage{Nodes: []CompanyGraphNode{}}
	for rows.Next() {
		if len(page.Nodes) == limit {
			page.HasMore = true
			break
		}
		var a assetRow
		var expandable bool
		if err := rows.Scan(&a.id, &a.kind, &a.domain, &a.rootDomain, &a.ip, &a.url, &a.port, &a.serviceType, &a.appName, &expandable); err != nil {
			return nil, err
		}
		key := assetKey(a.id)
		if a.id == 0 {
			key = "r:" + a.domain
		}
		page.Nodes = append(page.Nodes, CompanyGraphNode{Key: key, Kind: a.kind, Label: a.label(), Expandable: expandable})
	}
	return page, rows.Err()
}
