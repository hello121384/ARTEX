package agent

// cold-digest §4/§5/§7: the Compactor ties the pure algorithms (coldgraph.go)
// to the store (db/digest.go) and the LLM. It runs in two modes:
//
//	maintain — cheap, synchronous, once per planner round: bump round_no,
//	           recompute hot/cold, stamp/clear cold_since_round (§2.3). This is
//	           the bookkeeping the planner does anyway; it never calls the LLM.
//	minor/major — background, off the planner hot path (§7): group cold nodes
//	           and compress each ≥2 block into a digest via the LLM. minor folds
//	           only the not-yet-covered cold set (tiered append); major re-derives
//	           the whole grouping from source and merges fragments (§5.1/§5.2),
//	           reusing bodies whose signature is unchanged (§5.3).
//
// Concurrency: one compaction per task at a time (mutex), ≥cooldown between runs.
// The final graph, input and version check shares a serializable transaction with
// the digest write; conflicting writes abort the digest for a later retry.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// Compactor performs background cold-node compaction for many explorations.
type Compactor struct {
	prov            llm.Provider
	model           string
	params          coldParams
	n, m            int           // minor / major thresholds (§7 N=20, M=8)
	cooldown        time.Duration // min gap between compactions per task (§7 60s)
	failureCooldown time.Duration // suppress repeated calls for an unchanged failed block
	maxDur          time.Duration // hard cap on one background compaction

	mu       sync.Mutex
	running  map[int64]bool
	lastRun  map[int64]time.Time
	failures map[string]time.Time
}

const (
	maxLineageBlockMembers    = 4
	maxLineageBlockInputRunes = 8000
)

// NewCompactor builds a compactor. prov/model are used for the §4 body LLM call
// (same model the agent runs on, per §4). A nil Compactor is a safe no-op.
func NewCompactor(prov llm.Provider, model string) *Compactor {
	return &Compactor{
		prov:            prov,
		model:           model,
		params:          defaultColdParams(),
		n:               20,
		m:               8,
		cooldown:        60 * time.Second,
		failureCooldown: 10 * time.Minute,
		maxDur:          5 * time.Minute,
		running:         map[int64]bool{},
		lastRun:         map[int64]time.Time{},
		failures:        map[string]time.Time{},
	}
}

// OnPlannerRound is the single entry the planner calls each wake-up. It bumps the
// round, maintains the cold stamps synchronously, then (if a threshold is hit and
// no compaction is running / cooling down) launches a background compaction that
// outlives this planner round.
func (c *Compactor) OnPlannerRound(ctx context.Context, ts *db.ExplorationStore) {
	if c == nil || c.prov == nil || ts == nil {
		return
	}
	round, uncompressed, needMajor, err := c.maintain(ts)
	if err != nil {
		log.Printf("[compaction] maintain exp=%d: %v", ts.ID(), err)
		return
	}
	needMinor := uncompressed >= c.n
	if !needMinor && !needMajor {
		return
	}
	if !c.tryStart(ts.ID()) {
		return // already running, or within cooldown —派生态最终一致，下轮再压
	}
	go func() {
		defer c.finish(ts.ID())
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.maxDur)
		defer cancel()
		// 压缩是裸 provider 调用（compress 里直接 prov.Complete），不经过 agentcore
		// 的会话循环，所以 ctx 上没有 session id；按 session-id 头做提示缓存/粘性
		// 路由的网关（opencode zen 缺 x-opencode-session 直接 400）就收不到该头。
		// 这里补一个按探索稳定的 id：同一探索的所有压缩请求共享它，既能带上头，
		// 也让 llmrec 能把这次调用的 token 归因回该探索（此前记不到）。
		bg = transcript.WithSessionID(bg, fmt.Sprintf("exp%d-compactor", ts.ID()))
		if needMajor {
			c.major(bg, ts)
		} else {
			c.minor(bg, ts)
		}
	}()
	_ = round
}

// maintain bumps round_no, recomputes hot/cold over the whole graph, and applies
// the cold_since_round stamp/clear ops. Major is due only when the desired blocks
// actually differ from active digests, not merely because M digests exist.
func (c *Compactor) maintain(ts *db.ExplorationStore) (round int64, uncompressed int, needMajor bool, err error) {
	round, err = ts.BumpRound()
	if err != nil {
		return
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	hot := g.hotSet()
	structCold := g.structuralCold(hot)
	ops := computeStampOps(structCold, stamps, round)
	if err = ts.ApplyStampOps(toDBStampOps(ops)); err != nil {
		return
	}
	applyStampsInPlace(stamps, ops)
	elig := g.eligibleCold(structCold, stamps, round, c.params)
	covered, err := ts.CoveredMembers()
	if err != nil {
		return
	}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed++
		}
	}
	active, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	if len(active) >= c.m {
		versions, versionErr := ts.ContentVersions()
		if versionErr != nil {
			err = versionErr
			return
		}
		needMajor = c.majorNeeded(g, nodeByID, elig, versions, active)
	}
	return
}

func (c *Compactor) majorNeeded(g *coldGraph, nodeByID map[int64]*db.Node, eligible map[int64]bool, versions map[int64]int, active []*db.Node) bool {
	if len(active) < c.m {
		return false
	}
	desired := map[string]bool{}
	for _, b := range c.groupBlocks(g, eligible, nodeByID) {
		desired[blockSignature(b, versions)] = true
	}
	matched := map[string]bool{}
	for _, digest := range active {
		signature, _ := digestSigGen(digest)
		if !desired[signature] || matched[signature] {
			return true
		}
		matched[signature] = true
	}
	return len(matched) != len(desired)
}

// minor folds the not-yet-covered eligible-cold set into new digest segments
// (tiered append, §5). Existing digests are untouched.
func (c *Compactor) minor(ctx context.Context, ts *db.ExplorationStore) {
	round, err := ts.RoundNo()
	if err != nil {
		return
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	cvers, err := ts.ContentVersions()
	if err != nil {
		return
	}
	covered, err := ts.CoveredMembers()
	if err != nil {
		return
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	uncompressed := map[int64]bool{}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed[id] = true
		}
	}
	blocks := c.groupBlocks(g, uncompressed, nodeByID)
	if len(blocks) == 0 {
		return // this batch has no ≥2 connected/shared-parent block — nothing to fold (§7)
	}
	for _, b := range blocks {
		if ctx.Err() != nil {
			return
		}
		if c.failedBlock(b, cvers) {
			continue
		}
		err := c.foldBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, nil))
		c.noteBlockResult(b, cvers, err == nil)
	}
	// A minor may have pushed the segment count over M → merge in the same run.
	if ad, e := ts.ActiveDigests(); e == nil && len(ad) >= c.m {
		c.major(ctx, ts)
	}
}

// major re-derives the whole grouping from source over ALL eligible-cold nodes.
// Each replacement is generated before its old digest is touched and committed
// independently, so one failed block neither loses coverage nor blocks other blocks.
func (c *Compactor) major(ctx context.Context, ts *db.ExplorationStore) {
	round, err := ts.RoundNo()
	if err != nil {
		return
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	cvers, err := ts.ContentVersions()
	if err != nil {
		return
	}
	active, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	blocks := c.groupBlocks(g, elig, nodeByID)

	bySig := map[string]*db.Node{}
	for _, d := range active {
		sig, _ := digestSigGen(d)
		bySig[sig] = d
	}
	desired := map[string]bool{}
	var toCreate []block
	for _, b := range blocks {
		if ctx.Err() != nil {
			return
		}
		sig := blockSignature(b, cvers)
		desired[sig] = true
		if _, ok := bySig[sig]; ok {
			continue
		}
		toCreate = append(toCreate, b)
	}
	var stale []int64
	retained := map[string]bool{}
	for _, d := range active {
		sig, _ := digestSigGen(d)
		if !desired[sig] || retained[sig] {
			stale = append(stale, d.ID)
			continue
		}
		retained[sig] = true
	}
	pending := make(map[int64]bool, len(stale))
	for _, id := range stale {
		pending[id] = true
	}
	hasReplacement := map[int64]bool{}
	for _, b := range toCreate {
		if ctx.Err() != nil {
			return
		}
		if c.failedBlock(b, cvers) {
			continue
		}
		replacements := overlappingStaleDigests(active, pending, b.Members)
		for _, id := range replacements {
			hasReplacement[id] = true
		}
		prepared, err := c.prepareBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, active))
		if err != nil {
			log.Printf("[compaction] major prepare exp=%d block=%v: %v; keeping previous digest", ts.ID(), b.Members, err)
			c.noteBlockResult(b, cvers, false)
			continue
		}
		replacements = overlappingStaleDigests(active, pending, prepared.block.Members)
		creation := db.LineageDigestCreation{
			Payload:   prepared.payload,
			MemberIDs: prepared.block.Members,
			Check:     lineageDigestSnapshotCheck(ts, prepared.block, prepared.signature, prepared.input),
		}
		if err := ts.ReplaceLineageDigests(ctx, []db.LineageDigestCreation{creation}, replacements); err != nil {
			log.Printf("[compaction] major replace exp=%d block=%v: %v", ts.ID(), b.Members, err)
			c.noteBlockResult(b, cvers, false)
			continue
		}
		c.noteBlockResult(b, cvers, true)
		for _, id := range replacements {
			delete(pending, id)
		}
	}
	var orphaned []int64
	for id := range pending {
		if !hasReplacement[id] {
			orphaned = append(orphaned, id)
		}
	}
	if len(orphaned) > 0 {
		if err := ts.ReplaceLineageDigests(ctx, nil, orphaned); err != nil {
			log.Printf("[compaction] major retire exp=%d: %v", ts.ID(), err)
		}
	}
}

func overlappingStaleDigests(active []*db.Node, pending map[int64]bool, members []int64) []int64 {
	memberSet := make(map[int64]bool, len(members))
	for _, id := range members {
		memberSet[id] = true
	}
	var out []int64
	for _, digest := range active {
		if !pending[digest.ID] {
			continue
		}
		for _, member := range digestMemberIDs(digest) {
			if memberSet[member] {
				out = append(out, digest.ID)
				break
			}
		}
	}
	return out
}

type preparedLineageDigest struct {
	block     block
	payload   map[string]any
	signature string
	input     string
}

func (c *Compactor) foldBlock(ctx context.Context, ts *db.ExplorationStore, g *coldGraph, b block, nodeByID map[int64]*db.Node, cvers map[int64]int, generation int) error {
	prepared, err := c.prepareBlock(ctx, ts, g, b, nodeByID, cvers, generation)
	if err != nil {
		log.Printf("[compaction] compress exp=%d block=%v: %v", ts.ID(), b.Members, err)
		return err
	}
	if _, err := addLineageDigestChecked(ctx, ts, prepared.payload, prepared.block, prepared.signature, prepared.input); err != nil {
		log.Printf("[compaction] add digest exp=%d: %v", ts.ID(), err)
		return err
	}
	return nil
}

var errLineageSummaryIncomplete = errors.New("incomplete body from model")

func (c *Compactor) prepareBlock(ctx context.Context, ts *db.ExplorationStore, g *coldGraph, b block, nodeByID map[int64]*db.Node, cvers map[int64]int, generation int) (*preparedLineageDigest, error) {
	currentGraph, current, currentNodes, currentVersions := g, b, nodeByID, cvers
	for attempt := 0; attempt < 2; attempt++ {
		compressCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		body, err := c.compress(compressCtx, currentGraph, current, currentNodes)
		cancel()
		if err != nil {
			return nil, err
		}
		fresh, freshNodes, err := loadColdGraph(ts)
		if err != nil {
			return nil, err
		}
		freshVersions, err := ts.ContentVersions()
		if err != nil {
			return nil, err
		}
		freshHot := fresh.hotSet()
		members := make([]int64, 0, len(current.Members))
		for _, mID := range current.Members {
			if !freshHot[mID] {
				members = append(members, mID)
			}
		}
		if len(members) < c.params.K {
			return nil, db.ErrLineageDigestSnapshotChanged
		}
		final := block{Members: members, Anchors: current.Anchors}
		signature := blockSignature(final, freshVersions)
		if signature == blockSignature(current, currentVersions) {
			return &preparedLineageDigest{
				block:     final,
				payload:   digestPayload(body, final, freshNodes, generation, signature),
				signature: signature,
				input:     buildCompressionInput(currentGraph, current, currentNodes),
			}, nil
		}
		currentGraph, current, currentNodes, currentVersions = fresh, final, freshNodes, freshVersions
	}
	return nil, db.ErrLineageDigestSnapshotChanged
}

func lineageDigestSnapshotCheck(ts *db.ExplorationStore, b block, signature, input string) db.LineageDigestCheck {
	return func(tx *sql.Tx) error {
		g, nodes, err := loadColdGraphTx(ts, tx)
		if err != nil {
			return err
		}
		hot := g.hotSet()
		for _, id := range b.Members {
			if hot[id] {
				return db.ErrLineageDigestSnapshotChanged
			}
		}
		versions, err := ts.ContentVersionsTx(tx)
		if err != nil {
			return err
		}
		if blockSignature(b, versions) != signature || buildCompressionInput(g, b, nodes) != input {
			return db.ErrLineageDigestSnapshotChanged
		}
		return nil
	}
}

func addLineageDigestChecked(ctx context.Context, ts *db.ExplorationStore, payload map[string]any, b block, signature, input string) (int64, error) {
	return ts.AddLineageDigestChecked(ctx, payload, b.Members, lineageDigestSnapshotCheck(ts, b, signature, input))
}

// generationFor computes a digest's重摘代次 (§1): 1 for a fresh fold; for a major
// merge, max(generation) over the active digests that overlap this block's
// members, +1.
func (c *Compactor) generationFor(b block, active []*db.Node) int {
	if len(active) == 0 {
		return 1
	}
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	best := 0
	for _, d := range active {
		_, gen := digestSigGen(d)
		for _, m := range digestMemberIDs(d) {
			if memberSet[m] {
				if gen > best {
					best = gen
				}
				break
			}
		}
	}
	return best + 1
}

// tryStart acquires the per-task compaction lock, honoring the cooldown.
func (c *Compactor) tryStart(expID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[expID] {
		return false
	}
	if t, ok := c.lastRun[expID]; ok && time.Since(t) < c.cooldown {
		return false
	}
	c.running[expID] = true
	return true
}

func (c *Compactor) finish(expID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running[expID] = false
	c.lastRun[expID] = time.Now()
}

func (c *Compactor) failedBlock(b block, versions map[int64]int) bool {
	signature := blockSignature(b, versions)
	c.mu.Lock()
	defer c.mu.Unlock()
	failedAt, ok := c.failures[signature]
	return ok && c.failureCooldown > 0 && time.Since(failedAt) < c.failureCooldown
}

func (c *Compactor) noteBlockResult(b block, versions map[int64]int, success bool) {
	signature := blockSignature(b, versions)
	c.mu.Lock()
	defer c.mu.Unlock()
	if success {
		delete(c.failures, signature)
	} else {
		c.failures[signature] = time.Now()
	}
}

// --- helpers: db ↔ coldgraph ---

func (c *Compactor) groupBlocks(g *coldGraph, set map[int64]bool, nodeByID map[int64]*db.Node) []block {
	var out []block
	for _, b := range g.group(set, c.params) {
		out = append(out, c.splitBlocks(g, b, nodeByID)...)
	}
	return out
}

func (c *Compactor) splitBlocks(g *coldGraph, b block, nodeByID map[int64]*db.Node) []block {
	if len(b.Members) < c.params.K {
		return nil
	}
	var parts []block
	current := make([]int64, 0, min(len(b.Members), maxLineageBlockMembers))
	flush := func() {
		if len(current) == 0 {
			return
		}
		members := append([]int64(nil), current...)
		parts = append(parts, block{Members: members, Anchors: lineageBlockAnchors(g, members)})
		current = make([]int64, 0, min(len(b.Members), maxLineageBlockMembers))
	}
	for _, id := range b.Members {
		candidate := append(append([]int64(nil), current...), id)
		candidateBlock := block{Members: candidate, Anchors: lineageBlockAnchors(g, candidate)}
		tooLarge := len(candidate) > maxLineageBlockMembers || len([]rune(buildCompressionInput(g, candidateBlock, nodeByID))) > maxLineageBlockInputRunes
		if len(current) >= c.params.K && tooLarge {
			flush()
		}
		current = append(current, id)
	}
	flush()
	if len(parts) > 1 && len(parts[len(parts)-1].Members) < c.params.K {
		last := parts[len(parts)-1].Members[0]
		previous := parts[len(parts)-2].Members
		if len(previous) == c.params.K {
			parts[len(parts)-2].Members = append(previous, last)
			parts = parts[:len(parts)-1]
		} else {
			parts[len(parts)-2].Members = previous[:len(previous)-1]
			parts[len(parts)-1].Members = []int64{previous[len(previous)-1], last}
		}
	}
	for i := range parts {
		parts[i].Anchors = lineageBlockAnchors(g, parts[i].Members)
	}
	return parts
}

func lineageBlockAnchors(g *coldGraph, members []int64) []int64 {
	memberSet := make(map[int64]bool, len(members))
	for _, id := range members {
		memberSet[id] = true
	}
	anchorSet := map[int64]bool{}
	for _, id := range members {
		for _, parent := range g.parents[id] {
			if memberSet[parent] || g.nodes[parent].Kind == db.KindDigest {
				continue
			}
			anchorSet[parent] = true
		}
	}
	anchors := make([]int64, 0, len(anchorSet))
	for id := range anchorSet {
		anchors = append(anchors, id)
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i] < anchors[j] })
	return anchors
}

// loadColdGraph reads the exploration's nodes + edges and builds the cold-graph
// view plus an id→node index (for summaries/payload during compression).
func loadColdGraph(ts *db.ExplorationStore) (*coldGraph, map[int64]*db.Node, error) {
	g, nodes, _, err := loadColdGraphParts(ts)
	return g, nodes, err
}

func loadColdGraphParts(ts *db.ExplorationStore) (*coldGraph, map[int64]*db.Node, []db.Edge, error) {
	// Compaction must see the WHOLE graph, not the default row caps — pass a very
	// high limit so the LIMIT clause is effectively unbounded for real task sizes.
	const allRows = 1 << 30
	nodes, err := ts.Nodes(allRows)
	if err != nil {
		return nil, nil, nil, err
	}
	edges, err := ts.Edges(allRows)
	if err != nil {
		return nil, nil, nil, err
	}
	return buildColdGraph(nodes, edges), indexColdNodes(nodes), edges, nil
}

func loadColdGraphTx(ts *db.ExplorationStore, tx *sql.Tx) (*coldGraph, map[int64]*db.Node, error) {
	nodes, edges, err := ts.ReadGraphTx(tx)
	if err != nil {
		return nil, nil, err
	}
	return buildColdGraph(nodes, edges), indexColdNodes(nodes), nil
}

func indexColdNodes(nodes []*db.Node) map[int64]*db.Node {
	byID := make(map[int64]*db.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	return byID
}

func buildColdGraph(nodes []*db.Node, edges []db.Edge) *coldGraph {
	cgNodes := make([]cgNode, 0, len(nodes))
	for _, n := range nodes {
		cgNodes = append(cgNodes, cgNode{ID: n.ID, Kind: n.Kind, State: n.State})
	}
	cgEdges := make([]cgEdge, 0, len(edges))
	for _, e := range edges {
		cgEdges = append(cgEdges, cgEdge{From: e.From, Rel: e.Rel, To: e.To})
	}
	return newColdGraph(cgNodes, cgEdges)
}

func toDBStampOps(ops []stampOp) []db.StampOp {
	out := make([]db.StampOp, len(ops))
	for i, o := range ops {
		out[i] = db.StampOp{ID: o.ID, Set: o.Set, Round: o.Round}
	}
	return out
}

// applyStampsInPlace folds the just-applied ops into the in-memory stamp map so
// eligibility can be computed immediately without a re-read.
func applyStampsInPlace(stamps map[int64]*int64, ops []stampOp) {
	for _, o := range ops {
		if o.Set {
			r := o.Round
			stamps[o.ID] = &r
		} else {
			stamps[o.ID] = nil
		}
	}
}

// --- helpers: digest payload ---

func digestTopicTitle(body string) string {
	parts := strings.FieldsFunc(body, func(r rune) bool {
		return r == '\n' || r == '。' || r == '！' || r == '？' || r == '!' || r == '?'
	})
	title := ""
	if len(parts) > 0 {
		title = strings.TrimLeft(strings.TrimSpace(parts[0]), "#* ")
	}
	if title == "" {
		return "历史探索分组"
	}
	runes := []rune(title)
	if len(runes) > 48 {
		return string(runes[:48]) + "…"
	}
	return title
}

// digestPayload builds the digest node payload (cold-digest §1): the body, the
// member ids split by kind (restore cache; source of truth is the covers edges),
// the anchor ids, the generation, and the change-detection signature.
func digestPayload(body string, b block, nodeByID map[int64]*db.Node, generation int, signature string) map[string]any {
	var facts, intents []int64
	for _, m := range b.Members {
		if n := nodeByID[m]; n != nil && n.Kind == db.KindIntent {
			intents = append(intents, m)
		} else {
			facts = append(facts, m)
		}
	}
	return map[string]any{
		"title":      digestTopicTitle(body),
		"body":       body,
		"member_ids": map[string]any{"facts": facts, "intents": intents},
		"anchor_ids": b.Anchors,
		"generation": generation,
		"signature":  signature,
	}
}

func digestSigGen(n *db.Node) (string, int) {
	var p struct {
		Signature  string `json:"signature"`
		Generation int    `json:"generation"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.Signature, p.Generation
}

func digestMemberIDs(n *db.Node) []int64 {
	var p struct {
		MemberIDs struct {
			Facts   []int64 `json:"facts"`
			Intents []int64 `json:"intents"`
		} `json:"member_ids"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return append(append([]int64{}, p.MemberIDs.Facts...), p.MemberIDs.Intents...)
}

// --- helpers: compression input + LLM (§4) ---

func nodeSummary(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok {
			return s
		}
		if t, ok := p["text"].(string); ok {
			return t
		}
	}
	return ""
}

func nodeConfidence(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if c, ok := p["confidence"].(string); ok {
			return c
		}
	}
	return ""
}

func nodeConditions(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if conditions, ok := p["conditions"].(string); ok {
			return conditions
		}
	}
	return ""
}

// buildCompressionInput renders member summaries, evidence boundaries and
// lineage. Anchors provide context but are not compression members.
func buildCompressionInput(g *coldGraph, b block, nodeByID map[int64]*db.Node) string {
	memberSet := make(map[int64]bool, len(b.Members))
	intentCount, factCount := 0, 0
	for _, m := range b.Members {
		memberSet[m] = true
		if n := nodeByID[m]; n != nil && n.Kind == db.KindIntent {
			intentCount++
		} else {
			factCount++
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "【压缩范围】本块包含 %d 个 Intent、%d 条 Fact，共 %d 个节点。\n", intentCount, factCount, len(b.Members))
	sb.WriteString("【成员节点（要压缩的）】：\n")
	for _, m := range b.Members {
		n := nodeByID[m]
		kind := "fact"
		if n != nil && n.Kind == db.KindIntent {
			kind = "intent"
		}
		state := ""
		if n != nil {
			state = n.State
		}
		line := fmt.Sprintf("- #%d [%s/%s] %s", m, kind, state, nodeSummary(n))
		if conf := nodeConfidence(n); conf != "" {
			line += fmt.Sprintf(" (confidence=%s)", conf)
		}
		if conditions := nodeConditions(n); conditions != "" {
			line += fmt.Sprintf(" (conditions=%s)", conditions)
		}
		if n != nil && n.BlockedReason != "" {
			line += fmt.Sprintf(" (blocked_reason=%s)", n.BlockedReason)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	var edgeLines []string
	for _, m := range b.Members {
		for _, to := range g.children[m] {
			if memberSet[to] {
				edgeLines = append(edgeLines, fmt.Sprintf("- #%d 产出/派生→ #%d", m, to))
			}
		}
	}
	if len(edgeLines) > 0 {
		sb.WriteString("\n【成员之间的血缘边（父→子）】：\n")
		sort.Strings(edgeLines)
		sb.WriteString(strings.Join(edgeLines, "\n"))
		sb.WriteByte('\n')
	}
	if len(b.Anchors) > 0 {
		sb.WriteString("\n【共同父 / 上下文锚（不是成员，只用于理解这些结果从哪个意图探出）】：\n")
		for _, a := range b.Anchors {
			n := nodeByID[a]
			state := ""
			if n != nil {
				state = n.State
			}
			fmt.Fprintf(&sb, "- #%d [%s] %s\n", a, state, nodeSummary(n))
		}
	}
	return sb.String()
}

// compress runs the §4 body LLM call on one block. Uses the same model the agent
// runs on; thinking disabled (a pure summarization step). Only truncated output
// gets one shorter retry with more output room; provider failures and empty responses keep the block visible.
func (c *Compactor) compress(ctx context.Context, g *coldGraph, b block, nodeByID map[int64]*db.Node) (string, error) {
	input := buildCompressionInput(g, b, nodeByID)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		system := compressionSystemPrompt
		maxTokens := 600
		if attempt == 1 {
			system += compressionRetryPrompt
			maxTokens = 900
			log.Printf("[compaction] retry shorter summary block=%v: %v", b.Members, lastErr)
		}
		req := llm.CompletionRequest{
			System:    []string{system},
			Messages:  []llm.Message{llm.UserText(input)},
			MaxTokens: maxTokens,
			Thinking:  "disabled",
		}
		msg, stopReason, _, err := c.prov.Complete(ctx, req)
		if err != nil {
			return "", err
		}
		switch strings.ToLower(stopReason) {
		case "max_tokens", "length":
			lastErr = fmt.Errorf("%w: stop_reason=%s", errLineageSummaryIncomplete, stopReason)
			continue
		case "", "stop", "end_turn", "stop_sequence", "complete", "completed":
		default:
			return "", fmt.Errorf("invalid body completion: stop_reason=%s", stopReason)
		}
		body := strings.TrimSpace(msg.Text())
		if body == "" {
			return "", fmt.Errorf("empty body from model")
		}
		return body, nil
	}
	return "", lastErr
}

// compressionSystemPrompt is the §4 body prompt.
const compressionSystemPrompt = `把这组探索节点写成供 Planner 查重的导航摘要，不是证据文档。只输出 1–2 句、最多 220 字。第一句必须是可直接放入目录的“主题：结论”标题，不超过 40 字，明确最关键的正向发现、否定范围或未完成边界，不要用“本组”“本轮”“已针对”等过程词开头。第二句补充最关键限制，并引用最多 3 个代表来源 id。不要写覆盖数量，不要罗列接口、参数、响应字段、执行步骤或全部 Fact，这些信息由程序另行展示。observed 与 inferred 不得混淆，不得脑补。`

const compressionRetryPrompt = `
上一次输出达到长度上限。请直接给出完整的 1–2 句导航摘要，第一句用“主题：结论”且不超过 40 字，不解释、不复述过程、不逐条罗列成员；保留最关键的结论、限制或未完成边界及代表来源 id。`
