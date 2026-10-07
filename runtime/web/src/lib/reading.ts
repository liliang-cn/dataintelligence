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
  if (base === null) return `${name}：无基线`;
  if (now === null || !a.measured) return `${name}：暂无数据`;
  const down = target !== null ? target < base : false;
  const span = `${day(a.measured.from)}起 ${a.measured.data_days} 天`;
  parts.push(`${span}的${name}是 ${m.fmt(a.metric, now)}，基线 ${m.fmt(a.metric, base)}${target !== null ? `，达标线 ${m.fmt(a.metric, target)}` : ''}。`);
  const hit = target !== null && (down ? now <= target : now >= target);
  const better = down ? now < base : now > base;
  if (hit) parts.push('已达标。');
  else if (better) parts.push('好于基线，未达标。');
  else parts.push(now === base ? '与基线持平。' : '差于基线。');
  const broken = (a.guards ?? []).filter((g) => g.broken).map((g) => m.label(g.guard.metric));
  if (broken.length) parts.push(`护栏${broken.join('、')}破了。`);
  else if (a.guards?.length) parts.push('护栏守住。');
  if (!a.final) {
    parts.push(`已有 ${a.measured.data_days} 天数据。`);
  }
  return parts.join('');
}
