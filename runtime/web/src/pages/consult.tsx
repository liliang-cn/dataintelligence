import { useCallback, useEffect, useState } from 'react';
import { Link } from 'wouter';
import { MessagesSquareIcon, RefreshCwIcon } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { FindingCard } from '@/components/consult/finding';
import { GoalHeader } from '@/components/consult/goal';
import { PlanCard } from '@/components/consult/plan';
import { StageTrack } from '@/components/consult/stage-track';
import { api, ApiError, type Board, type GoalView } from '@/lib/api';
import { cn } from '@/lib/utils';
import { dayTime } from '@/lib/words';

function Section({ title, note, children }: { title: string; note?: string; children: React.ReactNode }) {
  return (
    <section className="border-t px-4 py-6 md:px-8 md:py-7">
      <h3 className="flex flex-wrap items-baseline gap-x-2 text-[15px] font-extrabold">
        {title}
        {note && <span className="text-[12.5px] font-normal text-muted-foreground">{note}</span>}
      </h3>
      <div className="mt-4">{children}</div>
    </section>
  );
}

function GoalCase({ gv, onChanged, loose }: { gv: GoalView; onChanged: () => void; loose?: boolean }) {
  const [lit, setLit] = useState<string | null>(null);
  const jump = (id: string) => {
    document.getElementById(`finding-${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    setLit(id);
    setTimeout(() => setLit(null), 1600);
  };
  const queries = gv.findings.reduce((n, f) => n + (f.evidence?.length ?? 0), 0);
  return (
    <article className="overflow-hidden rounded-3xl bg-card shadow-card ring-1 ring-border/70">
      {loose ? (
        <header className="px-5 pt-6 pb-5 md:px-8">
          <h2 className="text-[20px] font-extrabold">其他结论和计划</h2>
        </header>
      ) : (
        <>
          <StageTrack gv={gv} />
          <GoalHeader gv={gv} />
        </>
      )}
      <Section title="结论" note={gv.findings.length ? `${gv.findings.length} 条，背后是 ${queries} 次服务器执行过的查询` : undefined}>
        {gv.findings.length ? (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
            {gv.findings.map((f) => <FindingCard key={f.id} f={f} highlight={lit === f.id} />)}
          </div>
        ) : (
          <p className="text-[13.5px] text-muted-foreground">还没有结论</p>
        )}
      </Section>
      <Section title="计划" note={gv.plans.length ? `${gv.plans.length} 个` : undefined}>
        {gv.plans.length ? (
          <div className="grid gap-4">
            {gv.plans.map((pv) => <PlanCard key={pv.plan.id} pv={pv} findings={gv.findings} onChanged={onChanged} onJump={jump} />)}
          </div>
        ) : (
          <p className="text-[13.5px] text-muted-foreground">还没有计划</p>
        )}
      </Section>
    </article>
  );
}

function Loading() {
  return (
    <div className="overflow-hidden rounded-3xl bg-card ring-1 ring-border/70" aria-busy aria-label="正在加载">
      <div className="grid grid-cols-6 gap-4 border-b bg-surface/70 px-8 py-6">
        {Array.from({ length: 6 }, (_, i) => <Skeleton key={i} className="mx-auto size-7 rounded-full" />)}
      </div>
      <div className="grid gap-6 p-8 lg:grid-cols-[1fr_340px]">
        <div className="grid content-start gap-3">
          <Skeleton className="h-4 w-40" />
          <Skeleton className="h-8 w-4/5" />
          <Skeleton className="h-7 w-2/3" />
        </div>
        <Skeleton className="h-36 rounded-2xl" />
      </div>
      <div className="grid gap-3 border-t p-8 md:grid-cols-2">
        <Skeleton className="h-32 rounded-2xl" />
        <Skeleton className="h-32 rounded-2xl" />
      </div>
    </div>
  );
}

function Empty() {
  return (
    <div className="rounded-3xl bg-card px-6 py-14 text-center ring-1 ring-border/70">
      <div className="mx-auto grid size-12 place-items-center rounded-2xl bg-accent text-primary">
        <MessagesSquareIcon className="size-6" />
      </div>
      <h2 className="mt-4 text-[19px] font-extrabold">还没有咨询目标</h2>
      <Button asChild className="mt-6 h-10 rounded-xl px-5 font-bold">
        <Link href="/app/chat">去和顾问对话</Link>
      </Button>
    </div>
  );
}

export function ConsultPage() {
  const [board, setBoard] = useState<Board | null>(null);
  const [error, setError] = useState<string>('');
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const b = await api<Board>('/v1/consult');
      setBoard(b);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError && e.status === 404 ? '咨询档案未开启' : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const goals = board?.goals ?? [];
  const loose = (board?.loose_findings?.length ?? 0) + (board?.loose_plans?.length ?? 0) > 0;

  return (
    <main className="mx-auto max-w-[1240px] px-4 py-6 md:px-6 md:py-9">
      <div className="mb-6 flex flex-wrap items-end gap-x-6 gap-y-3 md:mb-8">
        <div className="mr-auto min-w-0">
          <h1 className="text-[26px] font-extrabold tracking-tight md:text-[30px]">咨询档案</h1>
        </div>
        <div className="flex items-center gap-2">
          {board && <span className="text-[12.5px] text-muted-foreground">更新于 {dayTime(board.at)}</span>}
          <Button variant="outline" size="icon" className="size-9 rounded-xl" onClick={load} aria-label="刷新">
            <RefreshCwIcon className={cn('size-4', loading && 'animate-spin')} />
          </Button>
        </div>
      </div>

      {error && !board && (
        <div className="rounded-2xl bg-danger-soft px-5 py-4 text-[14px] text-destructive">
          读不到咨询档案：{error}
          <Button variant="link" className="h-auto px-2 text-destructive" onClick={load}>再试一次</Button>
        </div>
      )}
      {!board && !error && <Loading />}
      {board && goals.length === 0 && !loose && <Empty />}
      <div className="grid gap-8">
        {goals.map((gv) => <GoalCase key={gv.goal.id} gv={gv} onChanged={load} />)}
        {board && loose && (
          <GoalCase
            loose
            gv={{ goal: { id: '', what: '', metric: '', direction: 'down', by: 0, within_days: 0, author: '', at: '' }, findings: board.loose_findings ?? [], plans: board.loose_plans ?? [] }}
            onChanged={load}
          />
        )}
      </div>
    </main>
  );
}
