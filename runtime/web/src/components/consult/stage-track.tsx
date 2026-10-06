import { CheckIcon } from 'lucide-react';
import type { GoalView } from '@/lib/api';
import { useModel } from '@/lib/model';
import { cn } from '@/lib/utils';
import { day, OUTCOME } from '@/lib/words';

type Stage = { name: string; done: boolean; now?: boolean; note: string };

export function stagesOf(gv: GoalView, label: (n: string) => string): Stage[] {
  const plans = gv.plans ?? [];
  const findings = gv.findings ?? [];
  const queries = findings.reduce((n, f) => n + (f.evidence ?? []).length, 0);
  const adopted = plans.filter((p) => p.decision?.verdict === 'adopted');
  const accepted = plans.filter((p) => p.acceptance);
  const s: Stage[] = [
    { name: '目标', done: !!gv.goal.metric, note: gv.goal.metric ? label(gv.goal.metric) : '' },
    { name: '调查', done: queries > 0, note: queries ? `${queries} 次查询` : '' },
    { name: '结论', done: findings.length > 0, note: findings.length ? `${findings.length} 条` : '' },
    { name: '计划', done: plans.length > 0, note: plans.length ? `${plans.length} 个` : '' },
    { name: '实施', done: adopted.length > 0, note: adopted.length ? `采纳 ${adopted.length} 个` : '等人采纳' },
    {
      name: '验收',
      done: accepted.length > 0,
      note: accepted.length ? OUTCOME[accepted[0].acceptance!.outcome]?.text ?? '' : adopted[0]?.due ? `${day(adopted[0].due)}到期` : '',
    },
  ];
  const i = s.findIndex((x) => !x.done);
  if (i >= 0) s[i].now = true;
  // nothing is "next" before the previous stage is done: notes for later stages stay quiet
  return s.map((x, j) => (i >= 0 && j > i ? { ...x, note: '' } : x));
}

/** The six stages every goal goes through, and where this one stands. */
export function StageTrack({ gv }: { gv: GoalView }) {
  const { label } = useModel();
  const stages = stagesOf(gv, label);
  const current = stages.find((s) => s.now);
  return (
    <div className="border-b bg-surface/70 px-4 pt-4 pb-3 md:px-8 md:pt-5 md:pb-4">
      <ol className="grid grid-cols-6" aria-label="进度">
        {stages.map((s, i) => {
          const next = stages[i + 1];
          return (
            <li key={s.name} className="relative flex flex-col items-center gap-1.5 text-center" aria-current={s.now ? 'step' : undefined}>
              {next && (
                <span
                  aria-hidden
                  className={cn('absolute top-[13px] left-[calc(50%+18px)] h-[3px] w-[calc(100%-36px)] rounded-full', next.done || next.now ? 'bg-primary' : 'bg-border')}
                />
              )}
              <span
                className={cn(
                  'relative z-10 grid size-[29px] place-items-center rounded-full text-[12px] font-extrabold tabular-nums',
                  s.done && 'bg-primary text-primary-foreground',
                  s.now && 'bg-card text-primary ring-[3px] ring-primary',
                  !s.done && !s.now && 'bg-card text-muted-foreground ring-2 ring-border',
                )}
              >
                {s.done ? <CheckIcon className="size-4 stroke-[3]" /> : i + 1}
                {s.now && <span className="absolute inset-0 animate-ping-slow rounded-full ring-2 ring-primary/40 motion-reduce:hidden" aria-hidden />}
              </span>
              <span className={cn('text-[13px] leading-tight font-semibold', s.now ? 'text-primary' : s.done ? 'text-foreground' : 'text-muted-foreground')}>{s.name}</span>
              <span className="min-h-[1.2em] w-full truncate px-1 text-[11.5px] leading-tight text-muted-foreground max-md:hidden">{s.note}</span>
            </li>
          );
        })}
      </ol>
      {current?.note && (
        <p className="mt-1 text-center text-[12px] text-muted-foreground md:hidden">
          现在：{current.name}，{current.note}
        </p>
      )}
    </div>
  );
}
