"use client";

import * as React from "react";

import type { Graph as G6Graph, GraphData } from "@antv/g6";
import { RotateCcw } from "lucide-react";

import { Button } from "@/components/ui/button";
import { type UITranslate, useUITranslation } from "@/i18n/use-ui-translation";
import { KIND_ICON, kindMeta } from "@/lib/asset-graph-appearance";
import type { CompanyGraphNode } from "@/lib/types";

export interface RelationshipCanvasNode {
  id: string;
  kind: CompanyGraphNode["kind"];
  label: string;
  parentId?: string;
  ownerLabel?: string;
  expandable?: boolean;
  expanded?: boolean;
}

type Point = { x: number; y: number };

// A branch occupies one row per visible leaf. Its parent sits between the
// first and last child, so branches do not cross when a page is appended.
function layout(nodes: RelationshipCanvasNode[]) {
  const positions = new Map<string, Point>();
  const children = new Map<string, RelationshipCanvasNode[]>();
  const roots = nodes.filter((node) => !node.parentId);
  for (const node of nodes) {
    if (!node.parentId) continue;
    const group = children.get(node.parentId);
    if (group) group.push(node);
    else children.set(node.parentId, [node]);
  }
  if (children.size === 0) {
    const columns = Math.min(4, Math.ceil(Math.sqrt(roots.length)));
    roots.forEach((node, i) => {
      positions.set(node.id, { x: (i % columns) * 200, y: Math.floor(i / columns) * 90 });
    });
    return positions;
  }
  let row = 0;
  function place(node: RelationshipCanvasNode, depth: number): number {
    const group = children.get(node.id);
    const childRows = group?.map((child) => place(child, depth + 1));
    const y = childRows?.length ? (childRows[0] + childRows[childRows.length - 1]) / 2 : row++ * 82;
    positions.set(node.id, { x: depth * 205, y });
    return y;
  }
  for (const root of roots) place(root, 0);
  return positions;
}

function shortLabel(value: string, limit = 28) {
  return value.length > limit ? `${value.slice(0, limit - 1)}…` : value;
}

function nodeLabel(node: RelationshipCanvasNode) {
  if (node.kind === "endpoint") {
    try {
      return shortLabel(new URL(node.label).pathname || "/");
    } catch {
      // Historic entries may already have a path or a plain-text label.
    }
  }
  return shortLabel(node.label);
}

function graphData(
  nodes: RelationshipCanvasNode[],
  positions: Map<string, Point>,
  selectedId: string | null | undefined,
  ui: UITranslate,
): GraphData {
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const path = new Set<string>();
  for (let id = selectedId; id && byId.has(id) && !path.has(id); id = byId.get(id)?.parentId) path.add(id);
  const kinds = kindMeta(ui);
  return {
    nodes: nodes.map((node) => {
      const { size, hex } = kinds[node.kind];
      const label = [nodeLabel(node), node.ownerLabel && shortLabel(node.ownerLabel, 22)].filter(Boolean).join("\n");
      return {
        id: node.id,
        style: {
          ...positions.get(node.id),
          size,
          fill: hex,
          stroke: path.has(node.id) ? "#0f766e" : hex,
          lineWidth: path.has(node.id) ? 3 : 1.5,
          opacity: path.size && !path.has(node.id) && node.parentId !== selectedId ? 0.82 : 1,
          iconSrc: KIND_ICON[node.kind],
          iconWidth: Math.max(12, size * 0.55),
          iconHeight: Math.max(12, size * 0.55),
          labelText: label,
          cursor: "pointer",
        },
      };
    }),
    edges: nodes
      .filter(
        (node): node is RelationshipCanvasNode & { parentId: string } => !!node.parentId && byId.has(node.parentId),
      )
      .map((node) => ({
        source: node.parentId,
        target: node.id,
        style: {
          stroke: path.has(node.id) ? "#0f766e" : "#94a3b8",
          lineWidth: path.has(node.id) ? 2.5 : 1.4,
          endArrow: true,
        },
      })),
  };
}

export function AssetRelationshipCanvas({
  nodes,
  selectedId,
  onNodeClick,
}: {
  nodes: RelationshipCanvasNode[];
  selectedId?: string | null;
  onNodeClick: (id: string) => void;
}) {
  const { ui } = useUITranslation();
  const host = React.useRef<HTMLDivElement>(null);
  const current = React.useRef({ nodes, selectedId, ui });
  const click = React.useRef(onNodeClick);
  const redraw = React.useRef<((reset?: boolean) => void) | null>(null);
  const [failed, setFailed] = React.useState(false);
  const [attempt, setAttempt] = React.useState(0);
  React.useEffect(() => {
    click.current = onNodeClick;
  }, [onNodeClick]);
  React.useEffect(() => {
    current.current = { nodes, selectedId, ui };
    redraw.current?.();
  }, [nodes, selectedId, ui]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: retry recreates the G6 renderer
  React.useEffect(() => {
    const container = host.current;
    if (!container) return;
    const surface = document.createElement("div");
    surface.className = "size-full";
    container.append(surface);
    let graph: G6Graph | undefined;
    let observer: ResizeObserver | undefined;
    let disposed = false;
    let drawing = false;
    let pending = true;
    let fit = true;
    let baseline = new Map<string, Point>();
    setFailed(false);

    async function draw(reset = false) {
      pending = true;
      fit ||= reset;
      if (!graph || drawing || disposed) return;
      drawing = true;
      try {
        while (pending && !disposed) {
          pending = false;
          const scene = current.current;
          const positions = layout(scene.nodes);
          // Anchor the expanded branch to its previous canvas position. Carry
          // across manual drags separately, without changing zoom or pan.
          const anchor = scene.selectedId ?? baseline.keys().next().value;
          const before = anchor && baseline.get(anchor);
          const after = anchor && positions.get(anchor);
          const delta = before && after ? { x: before.x - after.x, y: before.y - after.y } : { x: 0, y: 0 };
          const display = new Map<string, Point>();
          for (const [id, pos] of positions) {
            pos.x += delta.x;
            pos.y += delta.y;
            const old = baseline.get(id);
            if (old && graph.rendered) {
              const [x, y] = graph.getElementPosition(id);
              display.set(id, { x: pos.x + x - old.x, y: pos.y + y - old.y });
            } else display.set(id, pos);
          }
          baseline = positions;
          graph.setData(graphData(scene.nodes, display, scene.selectedId, scene.ui));
          if (graph.rendered) await graph.draw();
          else await graph.render();
          if (!disposed && fit && positions.size) {
            fit = false;
            await graph.fitView(undefined, false);
            if (!disposed && graph.getZoom() > 1) {
              await graph.zoomTo(1, false);
              if (!disposed) await graph.fitCenter(false);
            }
          }
        }
      } catch {
        if (!disposed) setFailed(true);
      } finally {
        drawing = false;
        if (disposed && !graph.destroyed) graph.destroy();
      }
    }

    void import("@antv/g6")
      .then(({ Graph }) => {
        if (disposed) return;
        graph = new Graph({
          container: surface,
          animation: false,
          padding: 32,
          zoomRange: [0.2, 3],
          node: {
            style: {
              labelPlacement: "bottom",
              labelFontSize: 12,
              labelFill: "#475569",
              labelBackground: true,
              labelBackgroundFill: "rgba(255,255,255,0.82)",
              labelBackgroundRadius: 3,
              labelPadding: [1, 3],
            },
          },
          behaviors: ["drag-element", "drag-canvas", "zoom-canvas"],
        });
        graph.on("node:click", (event) => {
          if ("targetType" in event && event.targetType === "node" && "id" in event.target)
            click.current(event.target.id);
        });
        observer = new ResizeObserver(([entry]) => {
          const { width, height } = entry.contentRect;
          if (!disposed && width && height) graph?.resize(width, height);
        });
        observer.observe(surface);
        redraw.current = draw;
        void draw();
      })
      .catch(() => {
        if (!disposed) setFailed(true);
      });
    return () => {
      disposed = true;
      redraw.current = null;
      observer?.disconnect();
      if (graph && !drawing && !graph.destroyed) graph.destroy();
      surface.remove();
    };
  }, [attempt]);

  return (
    <div className="relative h-full min-h-0 w-full overflow-hidden rounded-lg border bg-slate-100/80 dark:bg-slate-950/30">
      <div ref={host} className="absolute inset-0" />
      {failed ? (
        <div
          role="alert"
          className="absolute inset-0 grid place-content-center justify-items-center gap-3 bg-background/90"
        >
          {ui("assetGraph.canvasError")}
          <Button size="sm" variant="outline" onClick={() => setAttempt(attempt + 1)}>
            {ui("common.retry")}
          </Button>
        </div>
      ) : (
        <Button
          type="button"
          size="icon-sm"
          variant="outline"
          aria-label={ui("screen.764aba1c4a9d94")}
          title={ui("screen.764aba1c4a9d94")}
          className="absolute right-2 bottom-2 bg-background"
          onClick={() => redraw.current?.(true)}
        >
          <RotateCcw aria-hidden />
        </Button>
      )}
      <ul
        aria-label={ui("screen.1b1878ef15e2dd")}
        className="sr-only focus-within:not-sr-only focus-within:absolute focus-within:inset-x-2 focus-within:top-2 focus-within:max-h-40 focus-within:overflow-auto focus-within:rounded-md focus-within:border focus-within:bg-background focus-within:p-2"
      >
        {nodes.map((node) => (
          <li key={node.id}>
            <button
              type="button"
              onClick={() => click.current(node.id)}
              aria-expanded={node.expandable ? node.expanded : undefined}
            >
              {node.label} {node.ownerLabel}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
