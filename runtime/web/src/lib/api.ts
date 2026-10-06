// The console talks to the same /v1 API every other caller uses. The browser
// identifies itself with the di_token cookie (set on sign-in); API callers send
// the token as a Bearer header instead.

export class ApiError extends Error {
  status: number;
  rule?: string;
  constructor(status: number, message: string, rule?: string) {
    super(message);
    this.status = status;
    this.rule = rule;
  }
}

export async function api<T>(path: string, init?: RequestInit & { json?: unknown }): Promise<T> {
  const { json, ...rest } = init ?? {};
  const res = await fetch(path, {
    credentials: 'same-origin',
    ...rest,
    headers: { ...(json !== undefined ? { 'Content-Type': 'application/json' } : {}), ...rest.headers },
    body: json !== undefined ? JSON.stringify(json) : rest.body,
  });
  const text = await res.text();
  let body: any = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = null;
  }
  if (!res.ok) {
    const msg = body?.error ?? (res.status >= 500 ? '服务暂时不可用，稍后再试' : `请求失败（${res.status}）`);
    throw new ApiError(res.status, msg, body?.rule);
  }
  return body as T;
}

// ---- identity -------------------------------------------------------------

export type ConsoleInfo = { title: string; auth: 'users' | 'oidc' | 'users+oidc' | 'open'; copilot: boolean; consult: boolean };
export type Me = { user: string; role: string; auth: string };

const cookieOpts = () => `; path=/; SameSite=Strict${location.protocol === 'https:' ? '; Secure' : ''}`;

export function setToken(token: string) {
  document.cookie = `di_token=${encodeURIComponent(token.trim())}; max-age=${60 * 60 * 24 * 90}${cookieOpts()}`;
}
export function setDevUser(name: string) {
  document.cookie = `di_user=${encodeURIComponent(name.trim())}; max-age=${60 * 60 * 24 * 90}${cookieOpts()}`;
}
export function signOut() {
  document.cookie = `di_token=; max-age=0${cookieOpts()}`;
  document.cookie = `di_user=; max-age=0${cookieOpts()}`;
}

// ---- the semantic model ---------------------------------------------------

export type MetricInfo = { name: string; description: string; synonyms?: string[]; additivity?: string; roles?: string[] };
export type DimensionInfo = { name: string; type?: string; synonyms?: string[] };

// ---- the consulting loop ---------------------------------------------------

export type Filter = { dimension?: string; metric?: string; op: string; values?: unknown[]; and?: Filter[]; or?: Filter[] };
export type Guard = { metric: string; worse_is: 'up' | 'down'; tolerance?: number | null; limit?: number | null };
export type Measurement = {
  metric: string; scope?: Filter[]; from: string; to: string; value: number | null; data_days: number;
  sql: string; exec_ms: number; at: string;
};
export type Goal = {
  id: string; what: string; metric: string; direction: 'up' | 'down'; by: number; within_days: number;
  scope?: Filter[]; guards?: Guard[]; baseline?: Measurement; author: string; via?: string; at: string;
};
export type Evidence = {
  asked: string; sql: string; columns: string[]; rows: unknown[][]; row_count: number; truncated?: boolean;
  value?: number; exec_ms: number; at: string;
};
export type Finding = { id: string; goal?: string; says: string; evidence: Evidence[]; by: string; via?: string; at: string };
export type Action = { server: string; tool: string; args?: Record<string, unknown>; why?: string };
export type Plan = {
  id: string; goal?: string; findings: string[]; does: string;
  expect: { metric: string; direction: 'up' | 'down'; by: number; within_days: number; scope?: Filter[] };
  guards?: Guard[]; actions?: Action[]; cost?: string; by: string; via?: string; at: string; note?: string;
};
export type Decision = {
  plan: string; verdict: 'adopted' | 'rejected'; who: string; role?: string; why?: string; at: string; as_of: string;
  as_of_forced?: boolean; baseline?: Measurement; guard_baselines?: Measurement[];
};
export type ActionRun = {
  plan: string; index: number; server: string; tool: string; args?: Record<string, unknown>;
  result?: string; error?: string; skipped?: boolean; ms: number; at: string;
};
export type GuardResult = { guard: Guard; baseline: number | null; measured: Measurement | null; broken: boolean; why?: string };
export type Acceptance = {
  plan: string; final: boolean; metric: string; baseline: number | null; target: number | null; measured: Measurement | null;
  guards?: GuardResult[]; outcome: 'achieved' | 'missed' | 'guard_broken' | 'no_data'; says: string; by?: string; at: string;
};
export type PlanView = {
  plan: Plan; state: 'proposed' | 'adopted' | 'rejected' | 'accepted'; decision?: Decision; due?: string;
  actions?: ActionRun[]; progress?: Acceptance; acceptance?: Acceptance;
};
export type GoalView = { goal: Goal; target?: number; findings: Finding[]; plans: PlanView[] };
export type Board = { goals: GoalView[]; loose_findings?: Finding[]; loose_plans?: PlanView[]; at: string };

export type Verb = 'adopt' | 'reject' | 'measure' | 'accept';

export function decide(plan: string, verb: Verb, why?: string) {
  return api<any>(`/v1/consult/plans/${encodeURIComponent(plan)}/${verb}`, {
    method: 'POST',
    json: why ? { why } : {},
  });
}
