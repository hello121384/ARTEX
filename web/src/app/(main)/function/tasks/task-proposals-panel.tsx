"use client";

import * as React from "react";

import Link from "next/link";

import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Textarea } from "@/components/ui/textarea";
import { taskError } from "@/i18n/task-runtime";
import { useLiveTranslation } from "@/i18n/use-live-translation";
import { ApiError, http } from "@/lib/api";

type Asset = { id: number; label: string; type: string };
type Proposal = {
  id: number;
  version: number;
  company_id: number;
  company_name: string;
  goal: string;
  asset_ids: number[];
  assets?: Asset[];
  status: "pending" | "confirmed" | "withdrawn";
  task_id?: number;
  created_at: string;
};
type Match = {
  kind: "draft" | "task";
  id: number;
  goal: string;
  status: string;
  exact_goal: boolean;
  asset_ids: number[];
  explicit_asset_ids: number[];
  evidence_asset_ids: number[];
  created_at: string;
};
type Detail = {
  proposal: Proposal;
  assets?: Asset[];
  overlap: { matches: Match[]; unmatched_asset_ids: number[]; truncated: boolean } | null;
  review_key: string;
};
type Page = { proposals: Proposal[]; has_more: boolean };

const pageSize = 50;
const base = "/task-proposals";

function useProposalRead<T>(path: string) {
  const [attempt, setAttempt] = React.useState(0);
  const [response, setResponse] = React.useState<{ path: string; attempt: number; data?: T; error?: string }>();
  const { liveT } = useLiveTranslation();
  React.useEffect(() => {
    const controller = new AbortController();
    http<T>(path, { signal: controller.signal, cache: "no-store" }).then(
      (data) => {
        if (!controller.signal.aborted) setResponse({ path, attempt, data });
      },
      (e) => {
        if (!controller.signal.aborted) setResponse({ path, attempt, error: taskError(e, liveT) });
      },
    );
    return () => controller.abort();
  }, [path, attempt, liveT]);
  const current = response?.path === path && response?.attempt === attempt ? response : undefined;
  return { data: current?.data, error: current?.error, reload: () => setAttempt((value) => value + 1) };
}

export function TaskProposalsPanel({ onConfirmed }: { onConfirmed: () => void }) {
  const { t } = useLiveTranslation();
  const [id, setId] = React.useState<number | null>(null);
  const [offset, setOffset] = React.useState(0);
  const [createdId, setCreatedId] = React.useState<string | null>(null);
  const { data, error, reload } = useProposalRead<Page>(`${base}?limit=${pageSize}&offset=${offset}`);
  function back() {
    setId(null);
    reload();
  }

  if (id !== null) {
    return (
      <ProposalReview
        key={id}
        id={id}
        back={back}
        openDraft={setId}
        onCreated={(taskId) => {
          setCreatedId(taskId);
          onConfirmed();
          back();
        }}
      />
    );
  }
  return (
    <Card>
      <CardContent className="space-y-4 py-5">
        <div className="flex items-center justify-between gap-3">
          <h2 className="font-semibold">{t("taskProposals.list")}</h2>
          <Button variant="outline" size="sm" onClick={reload}>
            {t("taskProposals.refresh")}
          </Button>
        </div>
        {createdId && (
          <div role="status" className="text-sm">
            {t("taskProposals.created")} · <TaskLink id={createdId} />
          </div>
        )}
        {error && (
          <p role="alert" className="text-destructive">
            {error}
          </p>
        )}
        {!data && !error && <p role="status">{t("taskProposals.loading")}</p>}
        {data && !data.proposals?.length && <p className="text-muted-foreground">{t("taskProposals.empty")}</p>}
        <ul className="divide-y">
          {data?.proposals?.map((p) => (
            <li key={p.id} className="flex items-center justify-between gap-4 py-3">
              <div className="min-w-0">
                <p className="truncate font-medium" title={p.goal}>
                  {p.goal}
                </p>
                <p className="text-muted-foreground text-sm">
                  {t("taskProposals.draft", { id: p.id })} ·{" "}
                  {p.company_name || t("taskUI.companyId", { id: p.company_id })} ·{" "}
                  {t("taskProposals.count", { count: p.asset_ids.length })}
                </p>
              </div>
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setCreatedId(null);
                  setId(p.id);
                }}
              >
                {t("taskProposals.review")}
              </Button>
            </li>
          ))}
        </ul>
        {(offset > 0 || data?.has_more) && (
          <div className="flex gap-2">
            <Button variant="outline" disabled={!offset} onClick={() => setOffset(Math.max(0, offset - pageSize))}>
              {t("taskProposals.previous")}
            </Button>
            <Button variant="outline" disabled={!data?.has_more} onClick={() => setOffset(offset + pageSize)}>
              {t("taskProposals.next")}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function TaskLink({ id }: { id: number | string }) {
  const { t } = useLiveTranslation();
  return (
    <Link className="text-primary underline" href={`/function/tasks/detail?id=${encodeURIComponent(id)}`}>
      {t("taskProposals.task", { id })}
    </Link>
  );
}

type ReviewProps = {
  id: number;
  back: () => void;
  openDraft: (id: number) => void;
  onCreated: (taskId: string) => void;
};
function ProposalReview({ id, ...actions }: ReviewProps) {
  const { t } = useLiveTranslation();
  const { data, error, reload } = useProposalRead<Detail>(`${base}/${id}`);
  const [notice, setNotice] = React.useState("");
  if (!data)
    return (
      <Card>
        <CardContent className="space-y-3 py-5">
          <Button variant="outline" onClick={actions.back}>
            {t("taskProposals.back")}
          </Button>
          {error ? (
            <div role="alert">
              {error}{" "}
              <Button variant="link" onClick={reload}>
                {t("taskProposals.refresh")}
              </Button>
            </div>
          ) : (
            <p role="status">{t("taskProposals.loading")}</p>
          )}
        </CardContent>
      </Card>
    );
  return (
    <ReviewForm
      {...actions}
      data={data}
      notice={notice}
      refresh={(message) => {
        setNotice(message);
        reload();
      }}
    />
  );
}

function ReviewForm({
  data,
  back,
  openDraft,
  onCreated,
  notice,
  refresh,
}: Omit<ReviewProps, "id"> & {
  data: Detail;
  notice: string;
  refresh: (notice: string) => void;
}) {
  const { t, liveT, i18n } = useLiveTranslation();
  const { proposal: p, overlap, review_key: reviewKey } = data;
  const matches = overlap?.matches ?? [];
  const [goal, setGoal] = React.useState(p.goal);
  const [selected, setSelected] = React.useState(new Set(p.asset_ids));
  const [busy, setBusy] = React.useState(false);
  const lock = React.useRef(false);
  const editable = p.status === "pending";
  const dirty = goal !== p.goal || selected.size !== p.asset_ids.length;
  const labels = new Map((p.assets ?? data.assets ?? []).map((a) => [a.id, a.label]));
  const label = (id: number) => labels.get(id) || t("taskProposals.missing", { id });
  const occupied = new Set(
    matches
      .filter((m) => m.exact_goal && ["pending", "created", "queued", "running"].includes(m.status))
      .flatMap((m) => m.explicit_asset_ids ?? []),
  );
  const removable = Array.from(occupied).filter((id) => selected.has(id));
  const company = p.company_name || t("taskUI.companyId", { id: p.company_id });
  function navigate(action: () => void) {
    if (!lock.current && (!dirty || window.confirm(liveT("taskProposals.dismiss")))) action();
  }

  async function mutate(action: "save" | "withdraw" | "confirm") {
    if (lock.current) return;
    if (action === "withdraw" && !window.confirm(liveT("taskProposals.askWithdraw"))) return;
    lock.current = true;
    setBusy(true);
    try {
      const path = `${base}/${p.id}`;
      const bodies = {
        save: { version: p.version, goal: goal.trim(), asset_ids: p.asset_ids.filter((id) => selected.has(id)) },
        confirm: { version: p.version, review_key: reviewKey },
        withdraw: { version: p.version },
      };
      const result = await http<{ task_id: string; warning?: string }>(action === "save" ? path : `${path}/${action}`, {
        method: action === "save" ? "PATCH" : "POST",
        body: JSON.stringify(bodies[action]),
      });
      if (action === "confirm") {
        if (result.warning) toast.warning(liveT("taskProposals.queueWarning"));
        onCreated(result.task_id);
      } else if (action === "save") refresh("taskProposals.saved");
      else {
        toast.success(liveT("taskProposals.withdrawn"));
        back();
      }
    } catch (e) {
      if (e instanceof ApiError && e.code === "proposal_conflict") refresh("taskProposals.stale");
      else toast.error(taskError(e, liveT));
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardContent className="space-y-5 py-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="break-words font-semibold">
            {t("taskProposals.draft", { id: p.id })} · {company}
          </h2>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => navigate(back)}>
            {t("taskProposals.back")}
          </Button>
        </div>
        {notice && (
          <p role="status" className="rounded-md border px-3 py-2 text-sm">
            {t(notice)}
          </p>
        )}
        {!editable && (
          <p role="status">
            {t(p.status === "confirmed" ? "taskProposals.confirmed" : "taskProposals.withdrawn")}{" "}
            {p.task_id && <TaskLink id={p.task_id} />}
          </p>
        )}
        <label htmlFor="proposal-goal" className="block space-y-2">
          <span className="font-medium">{t("taskUI.goal")}</span>
          <Textarea
            id="proposal-goal"
            value={goal}
            onChange={(e) => setGoal(e.target.value)}
            disabled={!editable || busy}
          />
        </label>
        <fieldset className="space-y-2" disabled={!editable || busy}>
          <legend className="font-medium">
            {t("taskProposals.assets")} ({selected.size}/{p.asset_ids.length})
          </legend>
          <div className="flex max-h-60 flex-wrap gap-2 overflow-auto py-1">
            {p.asset_ids.map((id) => (
              <label
                htmlFor={`proposal-asset-${id}`}
                key={id}
                className="flex max-w-full items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm"
              >
                <Checkbox
                  id={`proposal-asset-${id}`}
                  checked={selected.has(id)}
                  disabled={!editable || busy}
                  onCheckedChange={(on) =>
                    setSelected((prev) => {
                      const next = new Set(prev);
                      if (on === true) next.add(id);
                      else next.delete(id);
                      return next;
                    })
                  }
                />
                <span className="break-all">{label(id)}</span>
              </label>
            ))}
          </div>
          {editable && removable.length > 0 && (
            <Button
              variant="outline"
              size="sm"
              disabled={busy || goal !== p.goal}
              onClick={() => setSelected((prev) => new Set([...prev].filter((id) => !occupied.has(id))))}
            >
              {t("taskProposals.remove", { count: removable.length })}
            </Button>
          )}
        </fieldset>
        <section className="space-y-2" aria-label={t("taskProposals.history")}>
          <h3 className="font-medium">{t("taskProposals.history")}</h3>
          {editable && !reviewKey && <p role="alert">{t("taskProposals.unavailable")}</p>}
          {dirty && (
            <p role="status" className="text-muted-foreground text-sm">
              {t("taskProposals.dirty")}
            </p>
          )}
          {matches.map((m) => {
            const facts = new Set(m.evidence_asset_ids ?? []);
            const explicit = new Set(m.explicit_asset_ids ?? []);
            const matchedIds = m.asset_ids ?? [];
            const scoped = matchedIds.filter((id) => !facts.has(id) && !explicit.has(id));
            const comparison = m.exact_goal ? "taskProposals.exact" : "taskProposals.different";
            const reason = matchedIds.length ? comparison : "taskProposals.textOnly";
            const state =
              m.status === "pending"
                ? t("taskProposals.pendingLabel")
                : t(`taskUI.${m.status === "completed" ? "done" : m.status}`, { defaultValue: m.status });
            return (
              <article key={m.kind + m.id} className="space-y-1 rounded-md border px-3 py-2 text-sm">
                <div className="flex flex-wrap items-baseline gap-2">
                  {m.kind === "task" ? (
                    <TaskLink id={m.id} />
                  ) : (
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => navigate(() => openDraft(m.id))}
                      className="text-primary underline"
                    >
                      {t("taskProposals.draft", { id: m.id })}
                    </button>
                  )}
                  <span>
                    {state} · {t(reason)}
                  </span>
                  {m.created_at && (
                    <time className="text-muted-foreground" dateTime={m.created_at}>
                      {new Date(m.created_at).toLocaleString(i18n.resolvedLanguage)}
                    </time>
                  )}
                </div>
                <p className="break-words">{m.goal}</p>
                {[
                  ["taskProposals.selected", [...explicit]],
                  ["taskProposals.evidence", [...facts]],
                  ["taskProposals.scope", scoped],
                ].map(([key, ids]) => {
                  if (!Array.isArray(ids) || !ids.length) return null;
                  return (
                    <p key={String(key)} className="break-words">
                      <span className="text-muted-foreground">{t(String(key))} · </span>
                      {ids.map(label).join(", ")}
                    </p>
                  );
                })}
              </article>
            );
          })}
          {overlap?.truncated && <p className="text-muted-foreground text-sm">{t("taskProposals.truncated")}</p>}
          <p className="break-words text-sm">
            {t("taskProposals.unmatched")} ·{" "}
            {overlap?.unmatched_asset_ids?.map(label).join(", ") || t("taskProposals.none")}
          </p>
        </section>
        {editable && (
          <div className="flex flex-wrap items-center justify-end gap-2 border-t pt-4">
            <Button variant="ghost" className="mr-auto" disabled={busy} onClick={() => void mutate("withdraw")}>
              {t("taskProposals.withdraw")}
            </Button>
            <Button
              variant="outline"
              disabled={busy || !dirty || !goal.trim() || !selected.size}
              onClick={() => void mutate("save")}
            >
              {t("taskProposals.save")}
            </Button>
            <Button disabled={busy || dirty || !selected.size || !reviewKey} onClick={() => void mutate("confirm")}>
              {t("taskProposals.confirm")}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
