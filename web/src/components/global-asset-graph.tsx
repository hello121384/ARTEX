"use client";

import * as React from "react";

import {
  BuildingIcon,
  ChevronDownIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  GlobeIcon,
  LayersIcon,
  LayoutTemplateIcon,
  LinkIcon,
  type LucideIcon,
  NetworkIcon,
  SmartphoneIcon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { useUITranslation } from "@/i18n/use-ui-translation";
import { api } from "@/lib/api";
import type { Company, CompanyGraphNode } from "@/lib/types";

const COMPANIES_PER_PAGE = 4;
const ROOTS_PER_PAGE = 6;
const CHILDREN_PER_PAGE = 20;
const ASSET_KINDS: Record<string, { icon: LucideIcon; label?: string }> = {
  root_domain: { icon: GlobeIcon, label: "screen.bc208f697acff9" },
  subdomain: { icon: GlobeIcon, label: "screen.4b4788120c34db" },
  ip: { icon: NetworkIcon },
  app: { icon: SmartphoneIcon, label: "screen.63c73c4730f447" },
  service: { icon: LayoutTemplateIcon, label: "screen.ec309ab207ef7f" },
  endpoint: { icon: LinkIcon, label: "screen.c80d519245a7e8" },
};

// Each child's horizontal edge meets the vertical spine on its parent's list.
const branchItem = "relative pt-1.5 pl-4 before:absolute before:top-5 before:left-0 before:w-4 before:border-t";
const branchList = "ml-3 border-l";

function AssetNode({ companyId, node }: { companyId: number; node: CompanyGraphNode }) {
  const { ui } = useUITranslation();
  const [open, setOpen] = React.useState(false);
  const meta = ASSET_KINDS[node.kind];
  const Icon = meta?.icon ?? LayersIcon;
  const kindLabel = meta?.label ? ui(meta.label) : node.kind.toUpperCase();
  const content = (
    <>
      <Icon aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="min-w-0 truncate font-mono">{node.label}</span>
      <span className="shrink-0 text-[10px] text-muted-foreground">{kindLabel}</span>
    </>
  );
  return (
    <li className={branchItem}>
      {node.expandable ? (
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="max-w-full"
          title={node.label}
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {content}
          {open ? <ChevronDownIcon aria-hidden /> : <ChevronRightIcon aria-hidden />}
        </Button>
      ) : (
        <span
          title={node.label}
          className="inline-flex h-7 max-w-full items-center gap-1.5 rounded-md border bg-background px-2.5 text-xs"
        >
          {content}
        </span>
      )}
      {open && <ChildAssets companyId={companyId} parent={node.key} />}
    </li>
  );
}

function ChildAssets({ companyId, parent }: { companyId: number; parent: string }) {
  const { ui } = useUITranslation();
  const [nodes, setNodes] = React.useState<CompanyGraphNode[]>([]);
  const [nextOffset, setNextOffset] = React.useState<number | null>(null);
  const [status, setStatus] = React.useState<"loading" | "ready" | "error">("loading");
  const [request, setRequest] = React.useState({ offset: 0 });

  React.useEffect(() => {
    let active = true;
    const limit = parent === "" ? ROOTS_PER_PAGE : CHILDREN_PER_PAGE;
    api
      .companyGraphChildren(companyId, parent, request.offset, limit)
      .then((page) => {
        if (!active) return;
        setNodes((previous) => {
          if (request.offset === 0) return page.nodes;
          const existing = new Set(previous.map((n) => n.key));
          return [...previous, ...page.nodes.filter((n) => !existing.has(n.key))];
        });
        setNextOffset(page.has_more && page.nodes.length ? request.offset + page.nodes.length : null);
        setStatus("ready");
      })
      .catch(() => {
        if (active) setStatus("error");
      });
    return () => {
      active = false;
    };
  }, [companyId, parent, request]);

  function load(offset: number) {
    setStatus("loading");
    setRequest({ offset });
  }

  return (
    <ul className={branchList} aria-busy={status === "loading"}>
      {nodes.map((node) => (
        <AssetNode key={node.key} node={node} companyId={companyId} />
      ))}
      {status === "loading" && (
        <li className={branchItem}>
          <span role="status" className="text-muted-foreground text-xs">
            {ui("common.loading")}
          </span>
        </li>
      )}
      {status === "error" && (
        <li className={branchItem}>
          <span role="status" className="mr-2 text-muted-foreground text-xs">
            {ui("assetGraph.loadError")}
          </span>
          <Button type="button" variant="outline" size="xs" onClick={() => load(request.offset)}>
            {ui("common.retry")}
          </Button>
        </li>
      )}
      {status === "ready" && nodes.length === 0 && (
        <li className={branchItem}>
          <span className="text-muted-foreground text-xs">
            {ui(parent === "" ? "assetGraph.emptyRoots" : "assetGraph.emptyBranch")}
          </span>
        </li>
      )}
      {status === "ready" && nextOffset !== null && (
        <li className={branchItem}>
          <Button type="button" variant="ghost" size="xs" onClick={() => load(nextOffset)}>
            {ui("assetGraph.more")}
          </Button>
        </li>
      )}
    </ul>
  );
}

type GraphCompany = Pick<Company, "id" | "name">;

function CompanyBranch({ company }: { company: GraphCompany }) {
  const { ui } = useUITranslation();
  const [open, setOpen] = React.useState(true);
  const [directOpen, setDirectOpen] = React.useState(false);
  return (
    <li className="min-w-0 rounded-lg border bg-card p-3">
      <Button
        type="button"
        variant="secondary"
        className="max-w-full"
        title={company.name}
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <BuildingIcon aria-hidden />
        <span className="truncate">{company.name}</span>
        {open ? <ChevronDownIcon aria-hidden /> : <ChevronRightIcon aria-hidden />}
      </Button>
      {open && (
        <>
          <ChildAssets companyId={company.id} parent="" />
          <ul className={branchList}>
            <li className={branchItem}>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                aria-expanded={directOpen}
                onClick={() => setDirectOpen(!directOpen)}
              >
                <LayersIcon aria-hidden />
                {ui("assetGraph.directAssets")}
                {directOpen ? <ChevronDownIcon aria-hidden /> : <ChevronRightIcon aria-hidden />}
              </Button>
              {directOpen && <ChildAssets companyId={company.id} parent="company" />}
            </li>
          </ul>
        </>
      )}
    </li>
  );
}

export function GlobalAssetGraph({ companies, company }: { companies: Company[]; company?: GraphCompany }) {
  const { ui } = useUITranslation();
  const [page, setPage] = React.useState(0);
  const lastPage = Math.max(0, Math.ceil(companies.length / COMPANIES_PER_PAGE) - 1);
  const currentPage = Math.min(page, lastPage);
  const first = currentPage * COMPANIES_PER_PAGE;
  const visible = company ? [company] : companies.slice(first, first + COMPANIES_PER_PAGE);
  if (!visible.length)
    return <p className="py-8 text-center text-muted-foreground text-sm">{ui("assetGraph.emptyCompanies")}</p>;
  return (
    <div className="min-w-0 space-y-3">
      {!company && lastPage > 0 && (
        <div className="flex items-center justify-between gap-2">
          <span className="text-muted-foreground text-xs">
            {ui("assetGraph.companyRange", { first: first + 1, last: first + visible.length, total: companies.length })}
          </span>
          <div className="flex gap-1">
            <Button
              type="button"
              size="icon-sm"
              variant="outline"
              disabled={currentPage === 0}
              aria-label={ui("common.previous")}
              onClick={() => setPage(currentPage - 1)}
            >
              <ChevronLeftIcon />
            </Button>
            <Button
              type="button"
              size="icon-sm"
              variant="outline"
              disabled={currentPage === lastPage}
              aria-label={ui("common.next")}
              onClick={() => setPage(currentPage + 1)}
            >
              <ChevronRightIcon />
            </Button>
          </div>
        </div>
      )}
      <div className="max-h-[62dvh] overflow-auto overscroll-contain rounded-lg bg-muted/40 p-2">
        <ul className="grid items-start gap-2">
          {visible.map((entry) => (
            <CompanyBranch key={entry.id} company={entry} />
          ))}
        </ul>
      </div>
    </div>
  );
}
