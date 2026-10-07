import { ArrowRightIcon, CornerDownRightIcon, ShieldCheckIcon } from 'lucide-react';
import type { Acceptance, Filter, GoalView, Guard } from '@/lib/api';
import { share, toNumber, useModel } from '@/lib/model';
import { cn } from '@/lib/utils';
import { day, OUTCOME, viaText } from '@/lib/words';

export function ScopeChips({ scope }: { scope?: Filter[] }) {
  const { scope: say } = useModel();
  if (!scope?.length) return null;
  return (
    <ul className="flex flex-wrap gap-1.5">
      {scope.map((f, i) => {
        const s = say(f);
        return (
          <li key={i} className="inline-flex items-baseline gap-1.5 rounded-lg bg-muted px-2.5 py-1 text-[13px]">
            <span className="text-muted-foreground">{s.dim}</span>
            <span className="font-semibold">{s.text}</span>
          </li>
        );
      })}
    </ul>
  );
}

export function GuardLines({ guards, className, bare }: { guards?: Guard[]; className?: string; bare?: boolean }) {
  const { guard } = useModel();
  if (!guards?.length) return null;
  return (
    <ul className={cn('grid gap-1', className)}>
      {guards.map((g, i) => (
        <li key={i} className="flex items-start gap-2 text-[13px] leading-snug">
          <ShieldCheckIcon className="mt-px size-4 shrink-0 text-warning" />
          <span>
            {!bare && <span className="text-muted-foreground">护栏：</span>}
            {guard(g)}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** The latest reading for the goal: an acceptance if one exists, else the last progress check. */
function latest(gv: GoalView): Acceptance | undefined {
  for (const p of gv.plans ?? []) {
    if (p.plan.expect.metric !== gv.goal.metric) continue;
    const a = p.acceptance ?? p.progress;
    if (a?.measured?.value != null) return a;
  }
  return undefined;
}

function HeadlineNumbers({ gv }: { gv: GoalView }) {
  const { label, fmt } = useModel();
  const g = gv.goal;
  const base = toNumber(g.baseline?.value);
  const target = toNumber(gv.target);
  const reading = latest(gv);
  const now = toNumber(reading?.measured?.value);
  let done: number | null = null;
  if (base !== null && target !== null && now !== null && base !== target) {
    done = Math.max(0, Math.min(1, (base - now) / (base - target)));
  }
  return (
    <div className="rounded-2xl bg-accent/60 p-4 md:p-5 lg:min-w-[340px]">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-[13px] font-bold text-accent-foreground">{label(g.metric)}</span>
        <span className="text-[12.5px] text-muted-foreground">
          {g.direction === 'down' ? '降' : '升'} {share(g.by)}，{g.within_days} 天内
        </span>
      </div>
      <div className="mt-2 flex items-end gap-3 md:gap-4">
        <div className="min-w-0">
          <div className="text-[34px] leading-none font-extrabold tracking-tight tabular-nums md:text-[40px]">{fmt(g.metric, base)}</div>
          <div className="mt-1.5 text-[12px] text-muted-foreground">基线</div>
        </div>
        <ArrowRightIcon className="mb-6 size-5 shrink-0 text-accent-foreground/60" />
        <div className="min-w-0">
          <div className="text-[34px] leading-none font-extrabold tracking-tight text-primary tabular-nums md:text-[40px]">{fmt(g.metric, target)}</div>
          <div className="mt-1.5 text-[12px] text-muted-foreground">达标线</div>
        </div>
      </div>
      {g.baseline && (
        <p className="mt-3 text-[12px] text-muted-foreground">
          {day(g.baseline.from)}–{day(g.baseline.to)}
        </p>
      )}
      {reading && now !== null && (
        <div className="mt-3 border-t border-primary/15 pt-3">
          <div className="flex items-baseline justify-between text-[12.5px]">
            <span>
              最新 <b className="tabular-nums">{fmt(g.metric, now)}</b>
              <span className="text-muted-foreground">（{day(reading.measured!.from)}起 {reading.measured!.data_days} 天）</span>
            </span>
            {done !== null && (done > 0 ? <b className="text-primary tabular-nums">走完 {Math.round(done * 100)}%</b> : <b className="text-muted-foreground">还没变好</b>)}
          </div>
          {done !== null && (
            <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-primary/15" role="progressbar" aria-valuenow={Math.round(done * 100)} aria-valuemin={0} aria-valuemax={100} aria-label="离达标线的进度">
              <div className="h-full rounded-full bg-primary" style={{ width: `${done * 100}%` }} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function nextStep(gv: GoalView): string {
  const plans = gv.plans ?? [];
  const accepted = plans.find((p) => p.acceptance);
  if (accepted) return `计划 ${accepted.plan.id} 已验收：${OUTCOME[accepted.acceptance!.outcome]?.text ?? '已出结论'}`;
  const adopted = plans.find((p) => p.state === 'adopted');
  if (adopted) return `计划 ${adopted.plan.id} 实施中${adopted.due ? `，${day(adopted.due)}验收` : ''}`;
  const proposed = plans.filter((p) => p.state === 'proposed');
  if (proposed.length) return `计划 ${proposed.map((p) => p.plan.id).join('、')} 待采纳`;
  return '';
}

function NextStep({ gv }: { gv: GoalView }) {
  return (
    <p className="mt-5 flex items-start gap-2 text-[13.5px] leading-relaxed">
      <CornerDownRightIcon className="mt-0.5 size-4 shrink-0 text-primary" />
      <span>{nextStep(gv)}</span>
    </p>
  );
}

export function GoalHeader({ gv }: { gv: GoalView }) {
  const g = gv.goal;
  return (
    <header className="grid gap-5 px-5 pt-6 pb-6 md:px-8 md:pt-7 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-start lg:gap-10">
      <div className="min-w-0">
        {g.author && (
          <p className="text-[12.5px] text-muted-foreground">
            {day(g.at)}由{g.author}提出{viaText(g.via) && `，${viaText(g.via)}`}
          </p>
        )}
        <h2 className="mt-1.5 text-[22px] leading-snug font-extrabold tracking-tight text-balance md:text-[26px]">{g.what}</h2>
        <div className="mt-4 grid gap-3">
          <ScopeChips scope={g.scope} />
          <GuardLines guards={g.guards} />
        </div>
        <NextStep gv={gv} />
      </div>
      {g.metric && <HeadlineNumbers gv={gv} />}
    </header>
  );
}
