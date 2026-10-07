import { useEffect, useRef, useState } from 'react';
import { Link } from 'wouter';
import {
  ArrowUpIcon, CheckIcon, ChevronDownIcon, FolderOpenIcon, HistoryIcon, LoaderCircleIcon, SquareIcon, Trash2Icon, TriangleAlertIcon,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { Textarea } from '@/components/ui/textarea';
import { Brand } from '@/components/brand';
import { Markdown } from '@/components/markdown';
import { useIsPhone } from '@/hooks/use-media';
import { useSession } from '@/lib/session';
import { cn } from '@/lib/utils';
import { dayTime, toolName, toolTouchesBoard, VERIFY_STEP } from '@/lib/words';

type Step = { tool: string; done: boolean; at: number; ms?: number };
type Turn = {
  id: string;
  question: string;
  at: string;
  status: 'running' | 'done' | 'error' | 'stopped';
  steps: Step[];
  thinking?: string;
  answer?: string;
  /** what the final check changed in the answer, one line each */
  corrected?: string[];
  /** the answer check could not run (the model was busy) */
  unchecked?: boolean;
  error?: string;
  ms?: number;
};

const KEY = 'di.copilot.turns.v1';
const STARTERS = [
  '一厂连杆线 OEE 为什么比曲轴线低？',
  '过去 30 天哪条线的废品率最高？主要是什么缺陷？',
  '停机时间主要花在哪些原因上？哪台设备最严重？',
  '想把一厂连杆线的废品率降 25%，该从哪里下手？',
];

function loadTurns(): Turn[] {
  try {
    const raw = localStorage.getItem(KEY);
    const turns: Turn[] = raw ? JSON.parse(raw) : [];
    return turns.map((t) => (t.status === 'running' ? { ...t, status: 'stopped' } : t));
  } catch {
    return [];
  }
}

function saveTurns(turns: Turn[]) {
  try {
    localStorage.setItem(KEY, JSON.stringify(turns.slice(-30)));
  } catch {
    /* storage full or blocked: the page still works, it just forgets */
  }
}

function seconds(ms?: number) {
  if (ms == null) return '';
  return ms < 1000 ? '不到 1 秒' : `${Math.round(ms / 1000)} 秒`;
}

/** Stream one copilot run. Events: thinking | tool_call | tool_result | verify | verified | complete | error. */
async function stream(question: string, signal: AbortSignal, on: (ev: { kind: string; tool?: string; text?: string }) => void) {
  const res = await fetch('/v1/copilot/stream', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ question }),
    signal,
  });
  if (!res.ok || !res.body) {
    let msg = `请求失败（${res.status}）`;
    try {
      msg = (await res.json()).error ?? msg;
    } catch {
      /* not JSON */
    }
    throw new Error(res.status === 401 ? '登录已失效' : msg);
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let i: number;
    while ((i = buf.indexOf('\n\n')) >= 0) {
      const chunk = buf.slice(0, i);
      buf = buf.slice(i + 2);
      const data = chunk.split('\n').filter((l) => l.startsWith('data:')).map((l) => l.slice(5).trim()).join('\n');
      if (!data) continue;
      try {
        on(JSON.parse(data));
      } catch {
        /* a partial or foreign line */
      }
    }
  }
}

function Activity({ turn }: { turn: Turn }) {
  const running = turn.status === 'running';
  const [open, setOpen] = useState(running);
  useEffect(() => {
    if (!running) setOpen(false);
  }, [running]);
  if (!turn.steps.length && !running) return null;
  return (
    <Collapsible open={open || running} onOpenChange={setOpen} className="rounded-2xl border bg-surface/60">
      <CollapsibleTrigger
        disabled={running}
        className="group flex w-full items-center gap-2.5 px-3.5 py-2.5 text-left text-[13px] outline-none focus-visible:ring-3 focus-visible:ring-ring/40 disabled:cursor-default"
      >
        {running ? <LoaderCircleIcon className="size-4 animate-spin text-primary" /> : <CheckIcon className="size-4 text-success" />}
        <span className="min-w-0 flex-1 truncate font-semibold">
          {running
            ? turn.steps.at(-1) && !turn.steps.at(-1)!.done
              ? turn.steps.at(-1)!.tool === VERIFY_STEP ? '核对中…' : `正在${toolName(turn.steps.at(-1)!.tool)}…`
              : '正在思考…'
            : `调用了 ${turn.steps.filter((s) => s.tool !== VERIFY_STEP).length} 次工具${turn.steps.some((s) => s.tool === VERIFY_STEP) ? '并核对了回答' : ''}，用时 ${seconds(turn.ms)}`}
        </span>
        {!running && <ChevronDownIcon className="size-4 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <ol className="grid gap-0.5 border-t px-3.5 py-2.5">
          {turn.steps.map((s, i) => (
            <li key={i} className="flex items-center gap-2.5 py-1 text-[13px]">
              <span className={cn('grid size-5 shrink-0 place-items-center rounded-full', s.done ? 'bg-success-soft text-success' : 'bg-accent text-primary')}>
                {s.done ? <CheckIcon className="size-3 stroke-[3]" /> : <LoaderCircleIcon className="size-3 animate-spin" />}
              </span>
              <span className={cn('flex-1', !s.done && 'font-semibold')}>{toolName(s.tool)}</span>
              {s.ms != null && <span className="text-[12px] text-muted-foreground tabular-nums">{seconds(s.ms)}</span>}
            </li>
          ))}
          {running && turn.thinking && (
            <li className="mt-1 line-clamp-2 text-[12.5px] leading-relaxed text-muted-foreground">{turn.thinking}</li>
          )}
        </ol>
      </CollapsibleContent>
    </Collapsible>
  );
}

function TurnView({ turn }: { turn: Turn }) {
  const touched = turn.steps.some((s) => toolTouchesBoard(s.tool));
  return (
    <div id={`turn-${turn.id}`} className="grid scroll-mt-24 gap-4">
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-2xl rounded-br-md bg-primary px-4 py-2.5 text-[14.5px] leading-relaxed text-primary-foreground md:max-w-[75%]">
          {turn.question}
        </div>
      </div>
      <div className="flex gap-3">
        <div className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-xl bg-card ring-1 ring-border max-md:hidden">
          <Brand size={20} />
        </div>
        <div className="grid min-w-0 flex-1 gap-3">
          <Activity turn={turn} />
          {turn.answer && (
            <div className="rounded-2xl bg-card px-4 py-4 ring-1 ring-border/70 md:px-5">
              <Markdown text={turn.answer} />
              {turn.unchecked && <p className="text-[12.5px] text-muted-foreground">核对未完成</p>}
              {turn.corrected && turn.corrected.length > 0 && (
                <div className="mt-4 border-t pt-3 text-[12.5px] leading-relaxed text-muted-foreground">
                  <p className="font-semibold">核对时改正了 {turn.corrected.length} 处：</p>
                  <ul className="mt-1 grid list-disc gap-0.5 pl-5">
                    {turn.corrected.map((c, i) => <li key={i}>{c}</li>)}
                  </ul>
                </div>
              )}
            </div>
          )}
          {turn.status === 'error' && (
            <div className="flex items-start gap-2 rounded-2xl bg-danger-soft px-4 py-3 text-[13.5px] text-destructive">
              <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" />
              <span>没有答完：{turn.error}</span>
            </div>
          )}
          {turn.status === 'stopped' && !turn.answer && <p className="text-[13px] text-muted-foreground">已停止</p>}
          {turn.status === 'done' && (
            <div className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted-foreground">
              <span>{dayTime(turn.at)}</span>
              <Button asChild variant={touched ? 'default' : 'outline'} size="sm" className="h-8 rounded-lg px-3 text-[12.5px] font-semibold">
                <Link href="/app/consult">
                  <FolderOpenIcon /> {touched ? '打开咨询档案' : '打开咨询档案'}
                </Link>
              </Button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function History({ turns, onPick, onClear }: { turns: Turn[]; onPick: (id: string) => void; onClear: () => void }) {
  return (
    <div className="grid gap-1">
      {[...turns].reverse().map((t) => (
        <button
          key={t.id}
          type="button"
          onClick={() => onPick(t.id)}
          className="grid gap-0.5 rounded-xl px-3 py-2 text-left outline-none transition-colors hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/40"
        >
          <span className="line-clamp-2 text-[13px] leading-snug font-medium">{t.question}</span>
          <span className="text-[11.5px] text-muted-foreground">{dayTime(t.at)}{t.status === 'error' ? '，没有答完' : ''}</span>
        </button>
      ))}
      {turns.length > 0 && (
        <Button variant="ghost" size="sm" className="mt-2 justify-start gap-2 rounded-lg text-muted-foreground" onClick={onClear}>
          <Trash2Icon /> 清空记录
        </Button>
      )}
    </div>
  );
}

export function ChatPage() {
  const { info } = useSession();
  const phone = useIsPhone();
  const [turns, setTurns] = useState<Turn[]>(loadTurns);
  const [draft, setDraft] = useState('');
  const [historyOpen, setHistoryOpen] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const endRef = useRef<HTMLDivElement>(null);
  const running = turns.some((t) => t.status === 'running');

  useEffect(() => saveTurns(turns), [turns]);
  useEffect(() => () => abort.current?.abort(), []);

  const update = (id: string, f: (t: Turn) => Turn) => setTurns((ts) => ts.map((t) => (t.id === id ? f(t) : t)));

  async function ask(q: string) {
    const question = q.trim();
    if (!question || running) return;
    const id = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
    const started = Date.now();
    setTurns((ts) => [...ts, { id, question, at: new Date().toISOString(), status: 'running', steps: [] }]);
    setDraft('');
    requestAnimationFrame(() => endRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' }));
    const ctl = new AbortController();
    abort.current = ctl;
    try {
      await stream(question, ctl.signal, (ev) => {
        if (ev.kind === 'thinking') update(id, (t) => ({ ...t, thinking: /[\u3400-\u9fff]/.test(ev.text ?? '') ? ev.text!.trim() : t.thinking }));
        else if (ev.kind === 'tool_call') update(id, (t) => ({ ...t, steps: [...t.steps, { tool: ev.tool ?? '', done: false, at: Date.now() }] }));
        else if (ev.kind === 'tool_result')
          update(id, (t) => {
            const steps = [...t.steps];
            const i = steps.findIndex((s) => !s.done && s.tool === ev.tool);
            const j = i >= 0 ? i : steps.findIndex((s) => !s.done);
            if (j >= 0) steps[j] = { ...steps[j], done: true, ms: Date.now() - steps[j].at };
            return { ...t, steps };
          });
        else if (ev.kind === 'verify') update(id, (t) => ({ ...t, steps: [...t.steps.map((s) => (s.done ? s : { ...s, done: true })), { tool: VERIFY_STEP, done: false, at: Date.now() }] }));
        else if (ev.kind === 'verify_skipped')
          update(id, (t) => ({ ...t, unchecked: true, steps: t.steps.map((s) => (s.tool === VERIFY_STEP && !s.done ? { ...s, done: true, ms: Date.now() - s.at } : s)) }));
        else if (ev.kind === 'verified')
          update(id, (t) => ({
            ...t,
            corrected: (ev.text ?? '').split('\n').map((x) => x.trim()).filter(Boolean),
            steps: t.steps.map((s) => (s.tool === VERIFY_STEP && !s.done ? { ...s, done: true, ms: Date.now() - s.at } : s)),
          }));
        else if (ev.kind === 'complete') {
          const text = ev.text ?? '';
          const failed = text.startsWith('error: ');
          update(id, (t) => ({
            ...t,
            status: failed ? 'error' : 'done',
            answer: failed ? undefined : text,
            error: failed ? text.slice(7) : undefined,
            steps: t.steps.map((s) => (s.done ? s : { ...s, done: true })),
            ms: Date.now() - started,
          }));
        } else if (ev.kind === 'error') update(id, (t) => ({ ...t, status: 'error', error: ev.text, ms: Date.now() - started }));
      });
      update(id, (t) => (t.status === 'running' ? { ...t, status: 'error', error: '连接已断开', ms: Date.now() - started } : t));
    } catch (e) {
      const stopped = (e as Error).name === 'AbortError';
      update(id, (t) => ({ ...t, status: stopped ? 'stopped' : 'error', error: stopped ? undefined : (e as Error).message, ms: Date.now() - started }));
    } finally {
      abort.current = null;
    }
  }

  const pick = (id: string) => {
    setHistoryOpen(false);
    setTimeout(() => document.getElementById(`turn-${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 50);
  };
  const clear = () => {
    setTurns([]);
    setHistoryOpen(false);
  };

  return (
    <main className="mx-auto grid max-w-[1240px] gap-6 px-4 md:px-6 lg:grid-cols-[260px_minmax(0,1fr)]">
      <aside className="sticky top-16 hidden h-[calc(100dvh-4rem)] py-8 lg:block" aria-label="提问记录">
        <h2 className="px-3 text-[13px] font-bold text-muted-foreground">问过的问题</h2>
        <ScrollArea className="mt-2 h-[calc(100%-2rem)] pr-2">
          <History turns={turns} onPick={pick} onClear={clear} />
        </ScrollArea>
      </aside>

      <div className="flex min-h-[calc(100dvh-4rem-76px)] min-w-0 flex-col md:min-h-[calc(100dvh-4rem)]">
        <div className="mx-auto flex w-full max-w-[820px] items-end gap-3 pt-6 md:pt-9">
          <div className="mr-auto">
            <h1 className="text-[26px] font-extrabold tracking-tight md:text-[30px]">顾问对话</h1>
          </div>
          <Button variant="outline" size="icon" className="size-9 shrink-0 rounded-xl lg:hidden" onClick={() => setHistoryOpen(true)} aria-label="问过的问题">
            <HistoryIcon className="size-4" />
          </Button>
        </div>

        <div className="flex-1 py-6">
          {turns.length === 0 ? (
            <div className="mx-auto max-w-[820px] pt-2 md:pt-4">
              <div className="mt-4 grid gap-2.5 sm:grid-cols-2">
                {STARTERS.map((s) => (
                  <button
                    key={s}
                    type="button"
                    disabled={!info.copilot}
                    onClick={() => ask(s)}
                    className="rounded-2xl border bg-card px-4 py-3.5 text-left text-[14px] leading-relaxed font-medium outline-none transition-colors hover:border-primary/50 hover:bg-accent/50 focus-visible:ring-3 focus-visible:ring-ring/40 disabled:opacity-50"
                  >
                    {s}
                  </button>
                ))}
              </div>
              {!info.copilot && <p className="mt-4 text-[13px] text-destructive">顾问对话未开启</p>}
            </div>
          ) : (
            <div className="mx-auto grid max-w-[820px] gap-9">
              {turns.map((t) => <TurnView key={t.id} turn={t} />)}
            </div>
          )}
          <div ref={endRef} />
        </div>

        <form
          className="sticky bottom-[calc(76px+env(safe-area-inset-bottom))] z-10 mx-auto w-full max-w-[820px] bg-linear-to-t from-stage from-70% to-transparent pt-5 pb-4 md:bottom-0 md:pb-6"
          onSubmit={(e) => {
            e.preventDefault();
            ask(draft);
          }}
        >
          <div className="flex items-end gap-2 rounded-2xl border bg-card p-2 shadow-float focus-within:border-primary/50">
            <Textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  ask(draft);
                }
              }}
              rows={1}
              placeholder={running ? '顾问正在回答…' : phone ? '问一个经营问题' : '输入问题'}
              disabled={!info.copilot}
              aria-label="你的问题"
              className="max-h-40 min-h-10 resize-none border-0 bg-transparent px-2.5 py-2 text-[15px] shadow-none focus-visible:ring-0 md:text-[14.5px] dark:bg-transparent"
            />
            {running ? (
              <Button type="button" size="icon" variant="secondary" className="size-10 shrink-0 rounded-xl" onClick={() => abort.current?.abort()} aria-label="停止">
                <SquareIcon className="size-3.5 fill-current" />
              </Button>
            ) : (
              <Button type="submit" size="icon" className="size-10 shrink-0 rounded-xl" disabled={!draft.trim() || !info.copilot} aria-label="发送">
                <ArrowUpIcon className="size-5" />
              </Button>
            )}
          </div>
        </form>
      </div>

      <Sheet open={historyOpen} onOpenChange={setHistoryOpen}>
        <SheetContent side="bottom" className="max-h-[80dvh] rounded-t-3xl pb-[env(safe-area-inset-bottom)]">
          <SheetHeader className="px-5 pt-5 pb-0">
            <SheetTitle className="text-base font-bold">问过的问题</SheetTitle>
          </SheetHeader>
          <div className="overflow-y-auto px-3 pb-4">
            <History turns={turns} onPick={pick} onClear={clear} />
          </div>
        </SheetContent>
      </Sheet>
    </main>
  );
}
