import { useState, type ReactNode } from 'react';
import {
  CheckIcon, CircleAlertIcon, CircleDashedIcon, FlagIcon, GavelIcon, LoaderCircleIcon, RulerIcon, SendIcon, XIcon,
} from 'lucide-react';
import { toast } from 'sonner';
import { Badge } from '@/components/ui/badge';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import { Button } from '@/components/ui/button';
import { Textarea } from '@/components/ui/textarea';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { Panel } from '@/components/panel';
import { ApiError, decide, type Finding, type PlanView, type Verb } from '@/lib/api';
import { share, useModel } from '@/lib/model';
import { reading } from '@/lib/reading';
import { cn } from '@/lib/utils';
import { actionArgs, actionFailure, actionReply, day, daysUntil, dayTime, OUTCOME, roleName, RULE, STATE, viaText } from '@/lib/words';
import { GuardLines, ScopeChips } from './goal';

const TONE = {
  warning: 'bg-warning-soft text-warning',
  primary: 'bg-accent text-accent-foreground',
  muted: 'bg-muted text-muted-foreground',
  success: 'bg-success-soft text-success',
  danger: 'bg-danger-soft text-destructive',
} as const;

export function StateBadge({ state }: { state: string }) {
  const s = STATE[state] ?? { text: state, tone: 'muted' as const };
  return <Badge className={cn('h-6 rounded-full px-2.5 text-[12px] font-bold', TONE[s.tone])}>{s.text}</Badge>;
}

function Fact({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[56px_minmax(0,1fr)] sm:gap-3">
      <dt className="pt-px text-[12.5px] text-muted-foreground">{term}</dt>
      <dd className="min-w-0 text-[13.5px] leading-relaxed">{children}</dd>
    </div>
  );
}

function Cites({ ids, findings, onJump }: { ids: string[]; findings: Finding[]; onJump: (id: string) => void }) {
  return (
    <span className="flex flex-wrap gap-1.5">
      {ids.map((id) => {
        const f = findings.find((x) => x.id === id);
        return (
          <Tooltip key={id}>
            <TooltipTrigger asChild>
              <button
                type="button"
                onClick={() => onJump(id)}
                className="rounded-md bg-accent px-1.5 py-0.5 text-[12px] font-extrabold text-accent-foreground tabular-nums outline-none hover:bg-primary hover:text-primary-foreground focus-visible:ring-3 focus-visible:ring-ring/40"
              >
                {id}
              </button>
            </TooltipTrigger>
            {f && <TooltipContent className="max-w-80 text-[12.5px] leading-relaxed">{f.says}</TooltipContent>}
          </Tooltip>
        );
      })}
    </span>
  );
}

function Actions({ pv }: { pv: PlanView }) {
  const actions = pv.plan.actions ?? [];
  if (!actions.length) return null;
  const ran = !!pv.decision;
  return (
    <section className="mt-5">
      <h4 className="text-[13px] font-bold">{ran ? '下发到现场的动作' : '采纳后会下发到现场的动作'}</h4>
      <ol className="mt-2.5 grid gap-2">
        {actions.map((a, i) => {
          const run = (pv.actions ?? []).find((r) => r.index === i + 1) ?? pv.actions?.[i];
          const failed = !!run?.error;
          const ok = run && !run.error && !run.skipped;
          return (
            <li key={i} className="flex gap-3 rounded-xl border bg-card p-3">
              <span
                className={cn(
                  'mt-px grid size-6 shrink-0 place-items-center rounded-full text-[11.5px] font-extrabold tabular-nums',
                  ok && 'bg-success text-white',
                  failed && 'bg-destructive text-white',
                  !run && 'bg-muted text-muted-foreground',
                  run?.skipped && 'bg-muted text-muted-foreground',
                )}
                aria-label={ok ? '已执行' : failed ? '失败' : '未执行'}
              >
                {ok ? <CheckIcon className="size-3.5 stroke-[3]" /> : failed ? <XIcon className="size-3.5 stroke-[3]" /> : i + 1}
              </span>
              <div className="min-w-0 flex-1">
                <p className="text-[13.5px] leading-relaxed">{a.why || '一个对现场的操作'}</p>
                {actionArgs(a.args).length > 0 && (
                  <div className="mt-1.5 flex flex-wrap gap-1">
                    {actionArgs(a.args).map(({ k, v }) => (
                      <span key={k} className="inline-flex items-baseline gap-1 rounded-md bg-muted px-1.5 py-0.5 text-[11.5px]">
                        <span className="text-muted-foreground">{k}</span>
                        <b className="font-semibold">{v}</b>
                      </span>
                    ))}
                  </div>
                )}
                {run && !failed && (
                  <p className={cn('mt-1.5 text-[12.5px] leading-relaxed', run.skipped ? 'text-muted-foreground' : 'text-success')}>
                    {run.skipped ? '没有执行' : `现场回复：${actionReply(run.result)}`}
                    {!run.skipped && <span className="text-muted-foreground">（{run.ms} 毫秒）</span>}
                  </p>
                )}
                {failed && (
                  <Collapsible className="mt-1.5">
                    <p className="text-[12.5px] leading-relaxed text-destructive">
                      没有下发成功：{actionFailure(run.error!)}
                      <CollapsibleTrigger className="ml-1.5 text-[12px] font-semibold text-muted-foreground underline-offset-4 outline-none hover:text-foreground hover:underline focus-visible:ring-3 focus-visible:ring-ring/40">
                        服务端原话
                      </CollapsibleTrigger>
                    </p>
                    <CollapsibleContent>
                      <p className="mt-1.5 rounded-lg bg-muted px-2.5 py-2 font-mono text-[11.5px] leading-relaxed break-all text-muted-foreground">{run.error}</p>
                    </CollapsibleContent>
                  </Collapsible>
                )}
              </div>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

function Step({ icon, title, done, last, children }: { icon: ReactNode; title: string; done: boolean; last?: boolean; children: ReactNode }) {
  return (
    <li className="relative grid grid-cols-[28px_minmax(0,1fr)] gap-3 pb-5 last:pb-0">
      {!last && <span aria-hidden className={cn('absolute top-8 bottom-1 left-[13px] w-0.5 rounded-full', done ? 'bg-primary/40' : 'bg-border')} />}
      <span className={cn('grid size-7 place-items-center rounded-full', done ? 'bg-primary text-primary-foreground' : 'bg-card text-muted-foreground ring-2 ring-border')}>{icon}</span>
      <div className="min-w-0 pt-0.5">
        <div className="text-[12.5px] font-bold text-muted-foreground">{title}</div>
        <div className="mt-1 text-[13.5px] leading-relaxed">{children}</div>
      </div>
    </li>
  );
}

function Record({ pv }: { pv: PlanView }) {
  const model = useModel();
  const { label, fmt } = model;
  const d = pv.decision;
  const acc = pv.acceptance;
  const left = pv.due ? daysUntil(pv.due) : null;
  return (
    <ol>
      <Step icon={<FlagIcon className="size-3.5" />} title="提出" done>
        <b>{pv.plan.by}</b>
        <div className="text-[12.5px] text-muted-foreground">{dayTime(pv.plan.at)}{viaText(pv.plan.via) && `，${viaText(pv.plan.via)}`}</div>
      </Step>
      <Step icon={<GavelIcon className="size-3.5" />} title={d?.verdict === 'rejected' ? '否决' : '采纳'} done={!!d}>
        {d ? (
          <>
            <b>{d.who}</b>
            <span className="text-muted-foreground">（{roleName(d.role)}）{d.verdict === 'adopted' ? '采纳' : '否决'}</span>
            <div className="text-[12.5px] text-muted-foreground">{dayTime(d.at)}</div>
            {d.why && <blockquote className="mt-2 rounded-r-lg border-l-[3px] border-primary/50 bg-card px-3 py-2 text-[13px] leading-relaxed">{d.why}</blockquote>}
            {d.baseline && (
              <dl className="mt-2.5 grid gap-1 text-[12.5px]">
                <div className="flex justify-between gap-3">
                  <dt className="text-muted-foreground">钉住的基线：{label(d.baseline.metric)}</dt>
                  <dd className="font-bold tabular-nums">{fmt(d.baseline.metric, d.baseline.value)}</dd>
                </div>
                {(d.guard_baselines ?? []).map((g, i) => (
                  <div key={i} className="flex justify-between gap-3">
                    <dt className="text-muted-foreground">护栏基线：{label(g.metric)}</dt>
                    <dd className="font-bold tabular-nums">{fmt(g.metric, g.value)}</dd>
                  </div>
                ))}
                <div className="text-muted-foreground">取自 {day(d.baseline.from)}至{day(d.baseline.to)}</div>
              </dl>
            )}
          </>
        ) : (
          <span className="text-muted-foreground">待审批</span>
        )}
      </Step>
      <Step icon={<RulerIcon className="size-3.5" />} title="验收" done={!!acc} last>
        {acc ? (
          <>
            <Badge className={cn('h-6 rounded-full px-2.5 text-[12px] font-bold', TONE[OUTCOME[acc.outcome]?.tone ?? 'muted'])}>{OUTCOME[acc.outcome]?.text ?? '已验收'}</Badge>
            <p className="mt-1.5">{reading(acc, model)}</p>
            {(acc.guards ?? []).map((g, i) => (
              <p key={i} className={cn('text-[12.5px]', g.broken ? 'text-destructive' : 'text-muted-foreground')}>
                {label(g.guard.metric)}{g.broken ? '破了' : '守住了'}：{fmt(g.guard.metric, g.baseline)} → {fmt(g.guard.metric, g.measured?.value)}
              </p>
            ))}
          </>
        ) : pv.due && left !== null ? (
          <>
            <div className="flex items-baseline gap-1.5">
              <b className="text-[26px] leading-none font-extrabold tabular-nums">{Math.max(0, left)}</b>
              <span className="text-muted-foreground">天后验收，{day(pv.due)}到期</span>
            </div>
            {pv.progress && (
              <p className="mt-2 rounded-lg bg-card px-3 py-2 text-[12.5px] leading-relaxed">
                <span className="font-semibold">{dayTime(pv.progress.at)} 测量</span>
                {reading(pv.progress, model)}
              </p>
            )}
          </>
        ) : (
          <span className="text-muted-foreground">{pv.state === 'rejected' ? '已否决' : `采纳后 ${pv.plan.expect.within_days} 天`}</span>
        )}
      </Step>
    </ol>
  );
}

type Ask = { verb: Verb; title: string; confirm: string; needWhy: boolean; whyLabel?: string; danger?: boolean };

function Decisions({ pv, onChanged }: { pv: PlanView; onChanged: () => void }) {
  const [ask, setAsk] = useState<Ask | null>(null);
  const [why, setWhy] = useState('');
  const [busy, setBusy] = useState<Verb | null>(null);
  const [tried, setTried] = useState(false);
  const model = useModel();
  const { label, fmt } = model;
  const n = pv.plan.actions?.length ?? 0;

  async function run(verb: Verb, reason?: string) {
    setBusy(verb);
    try {
      const res = await decide(pv.plan.id, verb, reason);
      if (verb === 'adopt') {
        const runs: { error?: string }[] = res.actions ?? [];
        const ok = runs.filter((r) => !r.error).length;
        const b = res.decision?.baseline;
        const pinned = b ? `基线 ${label(b.metric)} ${fmt(b.metric, b.value)} 已钉住。` : '';
        if (ok < runs.length) {
          toast.warning(`已采纳计划 ${pv.plan.id}，但有 ${runs.length - ok} 个动作没有下发成功`, { description: pinned });
        } else {
          toast.success(`已采纳计划 ${pv.plan.id}`, { description: `${pinned}${runs.length ? `${runs.length} 个动作已下发。` : ''}` });
        }
      } else if (verb === 'reject') {
        toast.success(`已否决计划 ${pv.plan.id}`);
      } else if (verb === 'measure') {
        toast.success('已测量', { description: reading(res, model) });
      } else {
        toast.success('已验收', { description: reading(res, model) });
      }
      setAsk(null);
      setWhy('');
      onChanged();
    } catch (e) {
      const err = e as ApiError;
      toast.error(RULE[err.rule ?? ''] ?? (err.status === 401 ? '需要先登录' : '没有执行'), { description: err.message });
    } finally {
      setBusy(null);
    }
  }

  if (pv.state !== 'proposed' && pv.state !== 'adopted') return null;
  return (
    <div className="flex flex-wrap items-center justify-end gap-2 border-t bg-surface/60 px-4 py-3 md:px-6">
      {pv.state === 'proposed' ? (
        <>
          <Button
            variant="outline"
            className="h-9 rounded-xl px-4 text-destructive hover:text-destructive max-sm:flex-1"
            onClick={() => setAsk({ verb: 'reject', title: `否决计划 ${pv.plan.id}`, confirm: '否决', needWhy: true, whyLabel: '否决理由（必填）', danger: true })}
          >
            否决
          </Button>
          <Button
            className="h-9 rounded-xl px-4 font-bold max-sm:flex-1"
            onClick={() => setAsk({ verb: 'adopt', title: `采纳计划 ${pv.plan.id}`, confirm: n ? '采纳并下发' : '采纳', needWhy: false, whyLabel: '采纳理由' })}
          >
            <SendIcon /> {n ? '采纳并下发' : '采纳'}
          </Button>
        </>
      ) : (
        <>
          <Button variant="outline" className="h-9 rounded-xl px-4 max-sm:flex-1" disabled={busy === 'measure'} onClick={() => run('measure')}>
            {busy === 'measure' ? <LoaderCircleIcon className="animate-spin" /> : <RulerIcon />} 量一下进度
          </Button>
          <Button
            variant="ghost"
            className="h-9 rounded-xl px-4 text-muted-foreground max-sm:flex-1"
            onClick={() => setAsk({ verb: 'accept', title: `提前验收计划 ${pv.plan.id}`, confirm: '现在验收', needWhy: false })}
          >
            提前验收
          </Button>
        </>
      )}
      {ask && (
        <Panel
          open
          onOpenChange={(o) => {
            if (!o) {
              setAsk(null);
              setTried(false);
            }
          }}
          title={ask.title}
          footer={
            <>
              <Button variant="outline" className="h-10 rounded-xl px-4" onClick={() => setAsk(null)}>取消</Button>
              <Button
                variant={ask.danger ? 'destructive' : 'default'}
                className="h-10 rounded-xl px-5 font-bold"
                disabled={!!busy}
                onClick={() => {
                  setTried(true);
                  if (ask.needWhy && !why.trim()) return;
                  run(ask.verb, why.trim() || undefined);
                }}
              >
                {busy === ask.verb && <LoaderCircleIcon className="animate-spin" />}
                {ask.confirm}
              </Button>
            </>
          }
        >
          <div className="grid gap-3">
            <p className="rounded-xl bg-muted px-3.5 py-3 text-[13.5px] leading-relaxed">{pv.plan.does}</p>
            {ask.whyLabel && (
              <div className="grid gap-1.5">
                <label htmlFor={`why-${pv.plan.id}`} className="text-[13px] font-semibold">{ask.whyLabel}</label>
                <Textarea
                  id={`why-${pv.plan.id}`}
                  autoFocus
                  rows={4}
                  value={why}
                  onChange={(e) => setWhy(e.target.value)}
                  placeholder="理由"
                  aria-invalid={tried && ask.needWhy && !why.trim()}
                  className="rounded-xl text-[14px]"
                />
                {tried && ask.needWhy && !why.trim() && <p className="text-[12.5px] text-destructive">请填写理由</p>}
              </div>
            )}
          </div>
        </Panel>
      )}
    </div>
  );
}

export function PlanCard({ pv, findings, onChanged, onJump }: { pv: PlanView; findings: Finding[]; onChanged: () => void; onJump: (id: string) => void }) {
  const { label } = useModel();
  const p = pv.plan;
  return (
    <article className="overflow-hidden rounded-2xl border bg-card">
      <div className="grid lg:grid-cols-[minmax(0,1fr)_320px]">
        <div className="min-w-0 p-4 md:p-6">
          <div className="flex items-center gap-2">
            <span className="text-[13px] font-bold text-muted-foreground tabular-nums">计划 {p.id}</span>
            <StateBadge state={pv.state} />
          </div>
          <p className="mt-2.5 text-[16px] leading-relaxed font-semibold text-pretty md:text-[17px]">{p.does}</p>
          <dl className="mt-4 grid gap-2.5">
            <Fact term="预期">
              {label(p.expect.metric)}
              {p.expect.direction === 'down' ? '下降' : '上升'} <b>{share(p.expect.by)}</b>，采纳后 {p.expect.within_days} 天验收
              {p.expect.scope?.length ? <div className="mt-1.5"><ScopeChips scope={p.expect.scope} /></div> : null}
            </Fact>
            {p.guards?.length ? (
              <Fact term="护栏">
                <GuardLines guards={p.guards} bare />
              </Fact>
            ) : null}
            {p.findings?.length ? (
              <Fact term="依据">
                <Cites ids={p.findings} findings={findings} onJump={onJump} />
              </Fact>
            ) : null}
            {p.cost && <Fact term="代价">{p.cost}</Fact>}
            {p.note && (
              <Fact term="提醒">
                <span className="inline-flex items-start gap-1.5 text-warning"><CircleAlertIcon className="mt-1 size-3.5 shrink-0" />{p.note}</span>
              </Fact>
            )}
          </dl>
          <Actions pv={pv} />
        </div>
        <aside className="border-t bg-surface/70 p-4 md:p-6 lg:border-t-0 lg:border-l" aria-label="决定记录">
          <h4 className="mb-4 flex items-center gap-1.5 text-[13px] font-bold">
            <CircleDashedIcon className="size-4 text-primary" /> 决定记录
          </h4>
          <Record pv={pv} />
        </aside>
      </div>
      <Decisions pv={pv} onChanged={onChanged} />
    </article>
  );
}
