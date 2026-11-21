import { AppWindow, Building2, Globe, Link2, type LucideIcon, Radio, Server, Waypoints } from "lucide-react";

import type { UITranslate } from "@/i18n/use-ui-translation";
import type { CoverageGraphNode } from "@/lib/types";

type Kind = CoverageGraphNode["kind"];
type KindMeta = { label: string; icon: LucideIcon; iconBg: string; hex: string; size: number };

export const kindMeta = (ui: UITranslate): Record<Kind, KindMeta> => ({
  company: { label: ui("screen.e06ff957ed48e8"), icon: Building2, iconBg: "bg-slate-500", hex: "#64748b", size: 46 },
  root_domain: { label: ui("screen.bc208f697acff9"), icon: Globe, iconBg: "bg-indigo-500", hex: "#6366f1", size: 38 },
  subdomain: { label: ui("screen.4b4788120c34db"), icon: Waypoints, iconBg: "bg-blue-500", hex: "#3b82f6", size: 30 },
  ip: { label: "IP", icon: Server, iconBg: "bg-cyan-600", hex: "#0891b2", size: 28 },
  service: { label: ui("screen.ec309ab207ef7f"), icon: Radio, iconBg: "bg-amber-500", hex: "#f59e0b", size: 26 },
  app: { label: "App", icon: AppWindow, iconBg: "bg-fuchsia-500", hex: "#d946ef", size: 26 },
  endpoint: { label: ui("screen.52fff1680de153"), icon: Link2, iconBg: "bg-rose-500", hex: "#f43f5e", size: 20 },
});

// G6 节点图标用平台一致的 lucide 图标：把 lucide 的 SVG 路径（v1.22）渲染成白色描边的
// data URI，作为节点 iconSrc（白色在实色/灰色底上都清晰）。手写内嵌，避免 react-dom/server
// 在 React19/Next 客户端打包的问题。
export function svgUri(inner: string, filled = false): string {
  const attrs = filled
    ? 'fill="#fff" stroke="none"'
    : 'fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" ${attrs}>${inner}</svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

export const KIND_ICON: Record<Kind, string> = {
  company: svgUri(
    '<path d="M10 12h4"/><path d="M10 8h4"/><path d="M14 21v-3a2 2 0 0 0-4 0v3"/><path d="M6 10H4a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-2"/><path d="M6 21V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v16"/>',
  ),
  root_domain: svgUri(
    '<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>',
  ),
  subdomain: svgUri(
    '<path d="m10.586 5.414-5.172 5.172"/><path d="m18.586 13.414-5.172 5.172"/><path d="M6 12h12"/><circle cx="12" cy="20" r="2"/><circle cx="12" cy="4" r="2"/><circle cx="20" cy="12" r="2"/><circle cx="4" cy="12" r="2"/>',
  ),
  ip: svgUri(
    '<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/>',
  ),
  service: svgUri(
    '<path d="M16.247 7.761a6 6 0 0 1 0 8.478"/><path d="M19.075 4.933a10 10 0 0 1 0 14.134"/><path d="M4.925 19.067a10 10 0 0 1 0-14.134"/><path d="M7.753 16.239a6 6 0 0 1 0-8.478"/><circle cx="12" cy="12" r="2"/>',
  ),
  app: svgUri(
    '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="M10 4v4"/><path d="M2 8h20"/><path d="M6 4v4"/>',
  ),
  endpoint: svgUri(
    '<path d="M9 17H7A5 5 0 0 1 7 7h2"/><path d="M15 7h2a5 5 0 1 1 0 10h-2"/><line x1="8" x2="16" y1="12" y2="12"/>',
  ),
};
