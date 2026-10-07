import { useEffect, useMemo, useState } from 'react';
import { PercentIcon, SearchIcon, SigmaIcon } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { api, type MetricInfo } from '@/lib/api';
import { useModel } from '@/lib/model';

const CJK = /[㐀-鿿]/;

function useCuts(metrics: MetricInfo[]) {
  const [cuts, setCuts] = useState<Record<string, string[]>>({});
  useEffect(() => {
    let live = true;
    const queue = metrics.map((m) => m.name);
    const worker = async () => {
      for (let name = queue.shift(); name; name = queue.shift()) {
        try {
          const r = await api<{ dimensions: string[] }>(`/v1/metrics/${encodeURIComponent(name)}/dimensions`);
          if (live) setCuts((c) => ({ ...c, [name!]: r.dimensions ?? [] }));
        } catch {
          if (live) setCuts((c) => ({ ...c, [name!]: [] }));
        }
      }
    };
    Promise.all(Array.from({ length: 6 }, worker));
    return () => {
      live = false;
    };
  }, [metrics]);
  return cuts;
}

function MetricCard({ m, extra, missing }: { m: MetricInfo; extra?: string[]; missing?: string[] }) {
  const { label, isShare } = useModel();
  const name = label(m.name);
  const aka = (m.synonyms ?? []).filter((s) => s !== name);
  const ratio = isShare(m.name) || m.additivity === 'non_additive';
  return (
    <li className="flex flex-col rounded-2xl bg-card p-4 ring-1 ring-border/70 md:p-5">
      <div className="flex items-center gap-2">
        <span className={ratio ? 'grid size-6 shrink-0 place-items-center rounded-md bg-maint-soft text-maint' : 'grid size-6 shrink-0 place-items-center rounded-md bg-accent text-primary'} aria-hidden>
          {ratio ? <PercentIcon className="size-3.5" /> : <SigmaIcon className="size-3.5" />}
        </span>
        <h3 className="text-[15px] font-bold">{name}</h3>
      </div>
      {CJK.test(m.description) && <p className="mt-2 text-[13.5px] leading-relaxed text-foreground/80">{m.description}</p>}
      {aka.length > 0 && <p className="mt-1 text-[12.5px] text-muted-foreground">也叫 {aka.join('、')}</p>}
      {extra === undefined ? (
        <Skeleton className="mt-3 h-5 w-1/2" />
      ) : extra.length > 0 || missing?.length ? (
        <div className="mt-auto grid gap-1.5 pt-3">
          {extra.length > 0 && (
            <ul className="flex flex-wrap items-center gap-1.5 border-t pt-3">
              <li className="text-[12px] text-muted-foreground">还能按</li>
              {extra.map((d) => (
                <li key={d} className="rounded-md bg-muted px-2 py-0.5 text-[12px] font-medium">{label(d)}</li>
              ))}
            </ul>
          )}
          {missing && missing.length > 0 && (
            <p className={extra.length ? 'text-[12px] text-muted-foreground' : 'border-t pt-3 text-[12px] text-muted-foreground'}>
              不能按{missing.map(label).join('、')}拆开
            </p>
          )}
        </div>
      ) : null}
    </li>
  );
}

export function MetricsPage() {
  const model = useModel();
  const [q, setQ] = useState('');
  const cuts = useCuts(model.metrics);
  const loaded = model.metrics.length > 0 && model.metrics.every((m) => cuts[m.name]);
  // the dimensions every metric shares are said once, not on every card
  const common = useMemo(() => {
    if (!loaded) return [];
    const lists = model.metrics.map((m) => cuts[m.name]);
    const count = new Map<string, number>();
    for (const l of lists) for (const d of l) count.set(d, (count.get(d) ?? 0) + 1);
    const order = model.dimensions.map((d) => d.name);
    return [...count.entries()]
      .filter(([, n]) => n >= lists.length * 0.7)
      .map(([d]) => d)
      .sort((x, y) => order.indexOf(x) - order.indexOf(y));
  }, [loaded, cuts, model.metrics]);
  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const hit = (m: MetricInfo) =>
      !needle || [model.label(m.name), m.description, ...(m.synonyms ?? [])].some((s) => s?.toLowerCase().includes(needle));
    const list = model.metrics.filter(hit);
    const ratio = (m: MetricInfo) => model.isShare(m.name) || m.additivity === 'non_additive';
    return [
      { title: '比率与效率', note: '不可直接相加', items: list.filter(ratio) },
      { title: '数量与时长', note: '可以按任意维度相加', items: list.filter((m) => !ratio(m)) },
    ].filter((g) => g.items.length);
  }, [model, q]);

  return (
    <main className="mx-auto max-w-[1100px] px-4 py-6 md:px-6 md:py-9">
      <div className="flex flex-wrap items-end gap-x-6 gap-y-4">
        <div className="mr-auto">
          <h1 className="text-[26px] font-extrabold tracking-tight md:text-[30px]">指标</h1>
        </div>
        <div className="relative w-full sm:w-72">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="搜索指标" className="h-10 rounded-xl bg-card pl-9" aria-label="找指标" />
        </div>
      </div>

      {!model.ready && (
        <div className="mt-8 grid gap-3">
          {Array.from({ length: 5 }, (_, i) => <Skeleton key={i} className="h-20 rounded-2xl" />)}
        </div>
      )}
      {common.length > 0 && (
        <div className="mt-6 flex flex-wrap items-center gap-1.5 rounded-2xl bg-accent/70 px-4 py-3">
          <span className="mr-1 text-[13px] font-semibold text-accent-foreground">指标大多能按这些拆开看</span>
          {common.map((d) => (
            <span key={d} className="rounded-md bg-card px-2 py-0.5 text-[12.5px] font-medium">{model.label(d)}</span>
          ))}
        </div>
      )}
      {model.ready && groups.length === 0 && (
        <p className="mt-10 text-center text-[14px] text-muted-foreground">{q ? `没有「${q}」` : '没有指标'}</p>
      )}
      {groups.map((g) => (
        <section key={g.title} className="mt-8">
          <h2 className="flex items-baseline gap-2 text-[15px] font-extrabold">
            {g.title}
            <span className="text-[12.5px] font-normal text-muted-foreground">{g.items.length} 个，{g.note}</span>
          </h2>
          <ul className="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
            {g.items.map((m) => (
              <MetricCard
                key={m.name}
                m={m}
                extra={loaded ? cuts[m.name].filter((d) => !common.includes(d)) : undefined}
                missing={loaded ? common.filter((d) => !cuts[m.name].includes(d)) : undefined}
              />
            ))}
          </ul>
        </section>
      ))}
    </main>
  );
}
