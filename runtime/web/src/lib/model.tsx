import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { api, type DimensionInfo, type Filter, type Guard, type MetricInfo } from './api';

// Names in the model are identifiers (scrap_rate, plant_name). People read the
// model's own Chinese synonyms instead; nothing here ever prints an identifier.

const CJK = /[㐀-鿿]/;

export type Model = {
  metrics: MetricInfo[];
  dimensions: DimensionInfo[];
  ready: boolean;
  label: (name: string) => string;
  isMetric: (name: string) => boolean;
  isShare: (name: string) => boolean;
  fmt: (name: string, v: unknown) => string;
  cell: (column: string, v: unknown) => string;
  scope: (f: Filter) => { dim: string; text: string };
  guard: (g: Guard) => string;
};

function firstCJK(list?: string[]) {
  return list?.find((s) => CJK.test(s));
}

/** The first clause of a description, as a last resort for a name with no Chinese synonym. */
function shortDescription(d?: string) {
  if (!d || !CJK.test(d)) return undefined;
  return d.split(/[。；，（(]/)[0].trim() || undefined;
}

export function toNumber(v: unknown): number | null {
  if (typeof v === 'number') return Number.isFinite(v) ? v : null;
  if (typeof v === 'string' && v.trim() !== '' && /^-?\d+(\.\d+)?(e-?\d+)?$/i.test(v.trim())) return Number(v);
  return null;
}

export function plainNumber(x: number) {
  if (x !== 0 && Math.abs(x) < 1) return x.toLocaleString('zh-CN', { maximumSignificantDigits: 3 });
  if (Math.abs(x) >= 100) return x.toLocaleString('zh-CN', { maximumFractionDigits: 0 });
  return x.toLocaleString('zh-CN', { maximumFractionDigits: 2 });
}

export function percent(x: number) {
  const p = x * 100;
  const d = Math.abs(p) < 10 ? 2 : 1;
  return `${p.toLocaleString('zh-CN', { minimumFractionDigits: d, maximumFractionDigits: d })}%`;
}

/** A relative change such as `by: 0.25` → 25%. */
export function share(by: number) {
  return `${Math.round(by * 1000) / 10}%`;
}

function build(metrics: MetricInfo[], dimensions: DimensionInfo[], ready: boolean): Model {
  const m = new Map(metrics.map((x) => [x.name, x]));
  const d = new Map(dimensions.map((x) => [x.name, x]));
  const label = (name: string) => {
    const mi = m.get(name);
    if (mi) return firstCJK(mi.synonyms) ?? shortDescription(mi.description) ?? '未命名指标';
    const di = d.get(name);
    if (di) return firstCJK(di.synonyms) ?? (di.type === 'time' ? '时间' : '分组');
    if (name === 'time' || name === 'day' || name === 'date') return '日期';
    return ready ? '数值' : '…';
  };
  const isShare = (name: string) => {
    const mi = m.get(name);
    if (mi && mi.additivity === 'non_additive' && label(name).includes('率')) return true;
    return /(rate|yield|oee|availability|performance)$/i.test(name);
  };
  const fmt = (name: string, v: unknown) => {
    const x = toNumber(v);
    if (x === null) return v === null || v === undefined ? '无数据' : String(v);
    if (isShare(name) && Math.abs(x) <= 1.5) return percent(x);
    return plainNumber(x);
  };
  const cell = (column: string, v: unknown) => {
    if (v === null || v === undefined || v === '') return '—';
    if (m.has(column)) return fmt(column, v);
    const s = String(v);
    const day = s.match(/^(\d{4}-\d{2}-\d{2})T00:00:00(\.0+)?(Z|[+-]00:?00)?$/);
    if (day) return day[1];
    const ts = s.match(/^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})/);
    if (ts) return `${ts[1]} ${ts[2]}`;
    const x = toNumber(v);
    return x === null ? s : plainNumber(x);
  };
  const values = (vs?: unknown[]) => (vs ?? []).map((x) => String(x)).join('、');
  const scope = (f: Filter): { dim: string; text: string } => {
    if (f.and || f.or) {
      const parts = (f.and ?? f.or ?? []).map((x) => {
        const s = scope(x);
        return `${s.dim}${s.text}`;
      });
      return { dim: f.and ? '同时满足' : '满足其一', text: parts.join(f.and ? '，且 ' : '，或 ') };
    }
    const dim = label(f.dimension ?? f.metric ?? '');
    const v = values(f.values);
    const op: Record<string, string> = {
      '=': v, in: v, '!=': `不含 ${v}`, 'not in': `不含 ${v}`, not_in: `不含 ${v}`,
      '>': `大于 ${v}`, '>=': `不早于 ${v}`, '<': `早于 ${v}`, '<=': `不晚于 ${v}`, like: `包含「${v}」`,
    };
    return { dim, text: op[f.op] ?? v };
  };
  const guard = (g: Guard) => {
    const name = label(g.metric);
    const parts: string[] = [];
    if (g.tolerance != null) parts.push(`${g.worse_is === 'down' ? '降幅' : '升幅'}不超过 ${share(g.tolerance)}`);
    if (g.limit != null) parts.push(`${g.worse_is === 'down' ? '不低于' : '不高于'} ${fmt(g.metric, g.limit)}`);
    return `${name}${parts.join('，且')}`;
  };
  return { metrics, dimensions, ready, label, isMetric: (n) => m.has(n), isShare, fmt, cell, scope, guard };
}

const Ctx = createContext<Model>(build([], [], false));

export function ModelProvider({ children, enabled }: { children: ReactNode; enabled: boolean }) {
  const [metrics, setMetrics] = useState<MetricInfo[]>([]);
  const [dims, setDims] = useState<DimensionInfo[]>([]);
  const [ready, setReady] = useState(false);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    Promise.allSettled([
      api<{ metrics: MetricInfo[] }>('/v1/metrics'),
      api<{ dimensions: DimensionInfo[] }>('/v1/dimensions'),
    ]).then(([a, b]) => {
      if (!live) return;
      if (a.status === 'fulfilled') setMetrics(a.value.metrics ?? []);
      if (b.status === 'fulfilled') setDims(b.value.dimensions ?? []);
      setReady(true);
    });
    return () => {
      live = false;
    };
  }, [enabled]);
  const value = useMemo(() => build(metrics, dims, ready), [metrics, dims, ready]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export const useModel = () => useContext(Ctx);
