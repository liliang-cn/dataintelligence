import { useState } from 'react';
import { ChevronRightIcon, CodeXmlIcon, TableIcon } from 'lucide-react';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { Panel } from '@/components/panel';
import type { Evidence, Finding } from '@/lib/api';
import { toNumber, useModel } from '@/lib/model';
import { cn } from '@/lib/utils';
import { dayTime } from '@/lib/words';

const SHOWN = 60;

function EvidenceTable({ e }: { e: Evidence }) {
  const { label, cell, isMetric } = useModel();
  const rows = e.rows ?? [];
  const numeric = e.columns.map((c, i) => isMetric(c) || rows.every((r) => r[i] == null || toNumber(r[i]) !== null));
  if (!rows.length) return <p className="rounded-xl bg-muted px-4 py-6 text-center text-sm text-muted-foreground">这次查询没有返回数据。</p>;
  return (
    <div className="overflow-hidden rounded-xl border">
      <div className="max-h-[52dvh] overflow-auto">
        <Table className="text-[13px]">
          <TableHeader className="sticky top-0 z-10 bg-surface">
            <TableRow className="hover:bg-transparent">
              {e.columns.map((c, i) => (
                <TableHead key={c} className={cn('h-9 px-3 font-semibold whitespace-nowrap text-muted-foreground', numeric[i] && 'text-right')}>
                  {label(c)}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.slice(0, SHOWN).map((r, ri) => (
              <TableRow key={ri}>
                {r.map((v, ci) => (
                  <TableCell key={ci} className={cn('px-3 py-2 whitespace-nowrap', numeric[ci] ? 'text-right font-medium tabular-nums' : 'font-medium')}>
                    {cell(e.columns[ci], v)}
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

function EvidencePanel({ e, open, onOpenChange }: { e: Evidence; open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Panel
      wide
      open={open}
      onOpenChange={onOpenChange}
      title={e.asked || '证据'}
      description={`${e.row_count} 行，查询用时 ${e.exec_ms} 毫秒，${dayTime(e.at)} 由服务器执行`}
    >
      <EvidenceTable e={e} />
      {(e.row_count > SHOWN || e.truncated) && (
        <p className="mt-2 text-[12px] text-muted-foreground">只列出前 {Math.min(SHOWN, e.rows.length)} 行，共 {e.row_count} 行。</p>
      )}
      <Collapsible className="mt-4">
        <CollapsibleTrigger className="group flex items-center gap-1.5 rounded-lg py-1 text-[13px] font-semibold text-muted-foreground outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/40">
          <ChevronRightIcon className="size-4 transition-transform group-data-[state=open]:rotate-90" />
          <CodeXmlIcon className="size-4" />
          服务器执行的 SQL
        </CollapsibleTrigger>
        <CollapsibleContent>
          <pre className="mt-2 max-h-[40dvh] overflow-auto rounded-xl bg-muted p-3.5 font-mono text-[11.5px] leading-relaxed text-foreground/85">{e.sql}</pre>
        </CollapsibleContent>
      </Collapsible>
    </Panel>
  );
}

export function FindingCard({ f, highlight }: { f: Finding; highlight?: boolean }) {
  const [open, setOpen] = useState<number | null>(null);
  return (
    <article id={`finding-${f.id}`} className={cn('flex min-w-0 scroll-mt-24 flex-col rounded-2xl border bg-card p-4 transition-shadow md:p-5', highlight && 'ring-3 ring-primary/40')}>
      <div className="flex gap-3">
        <span className="grid h-7 min-w-7 shrink-0 place-items-center rounded-lg bg-accent px-1.5 text-[13px] font-extrabold text-accent-foreground tabular-nums">{f.id}</span>
        <p className="pt-0.5 text-[14.5px] leading-relaxed">{f.says}</p>
      </div>
      {f.evidence?.length > 0 && (
        <ul className="mt-3.5 grid min-w-0 grid-cols-1 gap-1.5 md:pl-10">
          {f.evidence.map((e, i) => (
            <li key={i}>
              <button
                type="button"
                onClick={() => setOpen(i)}
                className="group flex w-full items-center gap-2.5 rounded-xl border bg-surface/60 px-3 py-2 text-left outline-none transition-colors hover:border-primary/40 hover:bg-accent/60 focus-visible:ring-3 focus-visible:ring-ring/40"
              >
                <TableIcon className="size-4 shrink-0 text-primary" />
                <span className="line-clamp-2 min-w-0 flex-1 text-[13px] leading-snug md:line-clamp-1">{e.asked || `证据 ${i + 1}`}</span>
                <span className="shrink-0 text-[12px] text-muted-foreground tabular-nums">{e.row_count} 行</span>
                <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
              </button>
              <EvidencePanel e={e} open={open === i} onOpenChange={(o) => setOpen(o ? i : null)} />
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}
