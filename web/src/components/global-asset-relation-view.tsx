"use client";

import * as React from "react";

import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";

import { AssetRelationshipCanvas, type RelationshipCanvasNode } from "@/components/asset-relationship-canvas";
import { Button } from "@/components/ui/button";
import { useUITranslation } from "@/i18n/use-ui-translation";
import { api } from "@/lib/api";
import type { Company, CompanyGraphNode } from "@/lib/types";

type Owner = Pick<Company, "id" | "name">;
type VisibleNode = RelationshipCanvasNode & { companyId: number; assetKey: string };
type BranchPage = { status: "loading" | "ready" | "error"; offset: number; hasMore: boolean };

const OWNERS_PER_PAGE = 4;
const ROOTS_PER_PAGE = 6;
const CHILDREN_PER_PAGE = 20;

function branchId(companyId: number, parent: string) {
  return `${companyId}/${parent}`;
}

function useBranchPages(owners: Owner[]) {
  const [rows, setRows] = React.useState<Record<string, CompanyGraphNode[]>>({});
  const pageRef = React.useRef<Record<string, BranchPage>>({});
  const [pages, setPages] = React.useState(pageRef.current);
  const mounted = React.useRef(false);

  const load = React.useCallback(async (companyId: number, parent: string, continuePage = false) => {
    const key = branchId(companyId, parent);
    const previous = pageRef.current[key];
    if (
      previous &&
      (!continuePage || previous.status === "loading" || (previous.status === "ready" && !previous.hasMore))
    )
      return;
    const offset = previous?.offset ?? 0;
    const mark = (page: BranchPage) => {
      pageRef.current = { ...pageRef.current, [key]: page };
      setPages(pageRef.current);
    };
    mark({ status: "loading", offset, hasMore: false });
    try {
      const limit = parent === "" ? ROOTS_PER_PAGE : CHILDREN_PER_PAGE;
      const next = await api.companyGraphChildren(companyId, parent, offset, limit);
      if (!mounted.current) return;
      setRows((current) => {
        const items = new Map((current[key] ?? []).map((node) => [node.key, node]));
        for (const node of next.nodes) items.set(node.key, node);
        return { ...current, [key]: Array.from(items.values()) };
      });
      mark({ status: "ready", offset: offset + next.nodes.length, hasMore: next.has_more && next.nodes.length > 0 });
    } catch {
      if (mounted.current) mark({ status: "error", offset, hasMore: false });
    }
  }, []);

  React.useEffect(() => {
    mounted.current = true;
    for (const owner of owners) void load(owner.id, "");
    return () => {
      mounted.current = false;
    };
  }, [owners, load]);

  return { rows, pages, load };
}

function BranchAction({
  page,
  onLoad,
  moreLabel,
  emptyLabel,
}: {
  page?: BranchPage;
  onLoad: () => void;
  moreLabel: string;
  emptyLabel: string;
}) {
  const { ui } = useUITranslation();
  if (!page || page.status === "loading") {
    return (
      <span role="status" className="text-muted-foreground text-xs">
        {ui("common.loading")}
      </span>
    );
  }
  if (page.status === "error") {
    return (
      <span className="inline-flex items-center gap-1">
        <span role="status" className="text-muted-foreground text-xs">
          {ui("assetGraph.loadError")}
        </span>
        <Button type="button" size="xs" variant="outline" onClick={onLoad}>
          {ui("common.retry")}
        </Button>
      </span>
    );
  }
  if (page.hasMore)
    return (
      <Button type="button" size="xs" variant="outline" onClick={onLoad}>
        {moreLabel}
      </Button>
    );
  if (page.offset === 0) return <span className="text-muted-foreground text-xs">{emptyLabel}</span>;
  return null;
}

function RelationScope({ owners }: { owners: Owner[] }) {
  const { ui } = useUITranslation();
  const { rows, pages, load } = useBranchPages(owners);
  const [expanded, setExpanded] = React.useState<Set<string>>(() => new Set());
  const [selectedId, setSelectedId] = React.useState<string | null>(null);

  const nodes = React.useMemo(() => {
    const visible: VisibleNode[] = [];
    const append = (owner: Owner, parent: string, parentId?: string) => {
      for (const asset of rows[branchId(owner.id, parent)] ?? []) {
        const id = branchId(owner.id, asset.key);
        const open = expanded.has(id);
        visible.push({
          id,
          parentId,
          assetKey: asset.key,
          companyId: owner.id,
          kind: asset.kind,
          label: asset.label,
          ownerLabel: !parentId && owners.length > 1 ? owner.name : undefined,
          expandable: asset.expandable,
          expanded: open,
        });
        if (open) append(owner, asset.key, id);
      }
    };
    for (const owner of owners) {
      append(owner, "");
      if (expanded.has(branchId(owner.id, "company"))) append(owner, "company");
    }
    return visible;
  }, [expanded, owners, rows]);

  function toggle(companyId: number, parent: string) {
    const id = branchId(companyId, parent);
    if (!expanded.has(id)) void load(companyId, parent);
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function clickNode(id: string) {
    const node = nodes.find((item) => item.id === id);
    if (!node) return;
    setSelectedId(id);
    if (node.expandable) toggle(node.companyId, node.assetKey);
  }
  const selected = nodes.find((node) => node.id === selectedId);
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex flex-wrap gap-x-4 gap-y-1.5">
        {owners.map((owner) => {
          const roots = branchId(owner.id, "");
          const direct = branchId(owner.id, "company");
          return (
            <div key={owner.id} className="flex max-w-full flex-wrap items-center gap-1.5">
              <span className="max-w-48 truncate font-medium text-xs" title={owner.name}>
                {owner.name}
              </span>
              <BranchAction
                page={pages[roots]}
                moreLabel={ui("assetGraph.loadMoreRoots")}
                emptyLabel={ui("assetGraph.emptyRoots")}
                onLoad={() => void load(owner.id, "", true)}
              />
              <Button
                type="button"
                size="xs"
                variant={expanded.has(direct) ? "secondary" : "ghost"}
                aria-expanded={expanded.has(direct)}
                onClick={() => toggle(owner.id, "company")}
              >
                {ui("assetGraph.directAssets")}
              </Button>
              {expanded.has(direct) && (
                <BranchAction
                  page={pages[direct]}
                  moreLabel={ui("assetGraph.loadMoreChildren")}
                  emptyLabel={ui("assetGraph.emptyBranch")}
                  onLoad={() => void load(owner.id, "company", true)}
                />
              )}
            </div>
          );
        })}
      </div>
      <div className="relative min-h-0 flex-1">
        <AssetRelationshipCanvas nodes={nodes} selectedId={selected?.id} onNodeClick={clickNode} />
      </div>
      {nodes.length > 0 && (
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <select
            className="h-7 max-w-full truncate rounded-md border border-input bg-background px-2 text-xs"
            aria-label={ui("assetGraph.selectNode")}
            value={selected?.id ?? ""}
            onChange={(event) => setSelectedId(event.target.value)}
          >
            <option value="" disabled>
              {ui("assetGraph.selectNode")}
            </option>
            {nodes.map((node) => (
              <option key={node.id} value={node.id}>
                {node.label}
                {owners.length > 1 ? ` · ${owners.find((owner) => owner.id === node.companyId)?.name}` : ""}
              </option>
            ))}
          </select>
          {!selected && <span className="text-muted-foreground text-xs">{ui("assetGraph.browseHint")}</span>}
          {selected?.expandable && (
            <Button
              type="button"
              size="xs"
              variant="outline"
              aria-expanded={expanded.has(selected.id)}
              onClick={() => toggle(selected.companyId, selected.assetKey)}
            >
              {ui(expanded.has(selected.id) ? "assetGraph.collapse" : "assetGraph.expand")}
            </Button>
          )}
          {selected && expanded.has(selected.id) && (
            <BranchAction
              page={pages[selected.id]}
              moreLabel={ui("assetGraph.loadMoreChildren")}
              emptyLabel={ui("assetGraph.emptyBranch")}
              onLoad={() => void load(selected.companyId, selected.assetKey, true)}
            />
          )}
        </div>
      )}
    </div>
  );
}

export function GlobalAssetRelationView({ companies, company }: { companies: Company[]; company?: Owner }) {
  const { ui } = useUITranslation();
  const [page, setPage] = React.useState(0);
  const lastPage = Math.max(0, Math.ceil(companies.length / OWNERS_PER_PAGE) - 1);
  const current = Math.min(page, lastPage);
  const first = current * OWNERS_PER_PAGE;
  const owners = React.useMemo(
    () => (company ? [company] : companies.slice(first, first + OWNERS_PER_PAGE)),
    [company, companies, first],
  );
  if (!owners.length)
    return <p className="py-8 text-center text-muted-foreground text-sm">{ui("assetGraph.emptyCompanies")}</p>;
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      {!company && lastPage > 0 && (
        <div className="flex items-center gap-1">
          <span className="mr-auto text-muted-foreground text-xs">
            {ui("assetGraph.companyRange", { first: first + 1, last: first + owners.length, total: companies.length })}
          </span>
          <Button
            type="button"
            size="icon-sm"
            variant="outline"
            disabled={current === 0}
            aria-label={ui("common.previous")}
            onClick={() => setPage(current - 1)}
          >
            <ChevronLeftIcon />
          </Button>
          <Button
            type="button"
            size="icon-sm"
            variant="outline"
            disabled={current === lastPage}
            aria-label={ui("common.next")}
            onClick={() => setPage(current + 1)}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      )}
      <RelationScope key={owners.map((owner) => owner.id).join(",")} owners={owners} />
    </div>
  );
}
