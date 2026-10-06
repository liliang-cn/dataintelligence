import type { Acceptance } from './api';
import { type Model, toNumber } from './model';
import { day } from './words';

// The server's own sentence for a progress check or an acceptance speaks in
// identifiers and raw fractions. The structured fields say the same thing;
// this says it in the console's words.
export function reading(a: Acceptance, m: Model): string {
  const name = m.label(a.metric);
  const base = toNumber(a.baseline);
  const target = toNumber(a.target);
  const now = toNumber(a.measured?.value);
  const parts: string[] = [];
  if (base === null) return `采纳时没有量到${name}的基线，无法比较。`;
  if (now === null || !a.measured) return `这段时间里量不到${name}，先不下结论。`;
  const down = target !== null ? target < base : false;
  const span = `${day(a.measured.from)}起 ${a.measured.data_days} 天`;
  parts.push(`${span}的${name}是 ${m.fmt(a.metric, now)}，基线 ${m.fmt(a.metric, base)}${target !== null ? `，达标线 ${m.fmt(a.metric, target)}` : ''}。`);
  const hit = target !== null && (down ? now <= target : now >= target);
  const better = down ? now < base : now > base;
  if (hit) parts.push('已经过了达标线。');
  else if (better) parts.push('在往好的方向走，还没到达标线。');
  else parts.push(now === base ? '和基线持平。' : '比基线还差。');
  const broken = (a.guards ?? []).filter((g) => g.broken).map((g) => m.label(g.guard.metric));
  if (broken.length) parts.push(`护栏${broken.join('、')}破了。`);
  else if (a.guards?.length) parts.push('护栏都守住了。');
  if (!a.final) {
    parts.push(a.measured.data_days < 7 ? `窗口刚开始，只有 ${a.measured.data_days} 天数据，先看趋势，不算结论。` : '窗口还没满，这是进度，不是验收结论。');
  }
  return parts.join('');
}
