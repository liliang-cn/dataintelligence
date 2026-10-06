// Everything the console says about the system, in plain Chinese.

export function day(t?: string) {
  if (!t) return '';
  const d = new Date(t.length === 10 ? `${t}T00:00:00` : t);
  if (Number.isNaN(d.getTime())) return t;
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return `${sameYear ? '' : `${d.getFullYear()}年`}${d.getMonth() + 1}月${d.getDate()}日`;
}

export function dayTime(t?: string) {
  if (!t) return '';
  const d = new Date(t);
  if (Number.isNaN(d.getTime())) return t;
  return `${day(t)} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

export function daysUntil(d: string) {
  return Math.ceil((new Date(`${d}T00:00:00`).getTime() - Date.now()) / 86400000);
}

export function roleName(r?: string) {
  return ({ approver: '审批人', analyst: '分析师', admin: '管理员', finance: '财务', manager: '经理' } as Record<string, string>)[r ?? ''] ?? '成员';
}

/** How a record got here, when it was not typed in by hand. */
export function viaText(via?: string) {
  return via === 'copilot' ? '通过顾问对话' : '';
}

export const STATE: Record<string, { text: string; tone: 'warning' | 'primary' | 'muted' | 'success' }> = {
  proposed: { text: '待采纳', tone: 'warning' },
  adopted: { text: '实施中', tone: 'primary' },
  rejected: { text: '已否决', tone: 'muted' },
  accepted: { text: '已验收', tone: 'success' },
};

export const OUTCOME: Record<string, { text: string; tone: 'success' | 'danger' | 'warning' | 'muted' }> = {
  achieved: { text: '达成', tone: 'success' },
  missed: { text: '没做到', tone: 'danger' },
  guard_broken: { text: '护栏破了', tone: 'warning' },
  no_data: { text: '没有数据', tone: 'muted' },
};

/** A refusal's rule, as the headline of the toast that explains it. */
export const RULE: Record<string, string> = {
  self_approval: '不能采纳自己提出的计划',
  role_not_allowed: '你的角色不能做这个决定',
  anonymous: '需要先登录',
  already_decided: '这个计划已经有人决定过了',
  why_required: '要写明理由',
  not_adopted: '计划还没有被采纳',
  too_early: '还没到验收的时候',
  already_accepted: '这个计划已经验收过了',
  no_baseline: '量不出基线',
  as_of_not_allowed: '不能指定历史日期',
  not_found: '找不到这条记录',
  bad_action: '计划里的动作无法执行',
  unknown_metric: '模型里没有这个指标',
  vague_goal: '目标说得不够清楚',
  vague_expectation: '预期效果说得不够清楚',
  bad_guard: '护栏设置有问题',
  bad_scope: '范围设置有问题',
  finding_without_evidence: '结论没有证据',
  evidence_query_failed: '证据查询失败',
  plan_without_finding: '计划没有引用结论',
  unknown_finding: '引用了不存在的结论',
  no_time_dimension: '指标没有时间维度',
};

/** Copilot tools, by what they do for the person watching. */
const TOOLS: Record<string, string> = {
  describe_warehouse: '查看数据表',
  list_metrics: '浏览指标',
  get_dimensions: '查看可拆分的维度',
  query_metric: '查询指标',
  health_check: '数据体检',
  consult_list: '读取咨询档案',
  consult_add_goal: '记录目标',
  consult_add_finding: '记录结论',
  consult_propose_plan: '提出计划',
  list_sites: '查看工厂清单',
  plant_status: '读取工厂实况',
  list_alarms: '查看报警',
  get_asset: '查看设备',
  read_tags: '读取设备数据点',
  list_sources: '查看数据接入',
  simulate_plan: '孪生推演',
  send_command: '下发指令',
};

/** The copilot checking its own answer against what its tools returned. */
export const VERIFY_STEP = '__verify';

export function toolName(t?: string) {
  if (!t) return '思考';
  if (t === VERIFY_STEP) return '核对回答里的名称和数字';
  const bare = t.includes('__') ? t.split('__').pop()! : t;
  return TOOLS[bare] ?? TOOLS[t] ?? (t.includes('__') ? '调用外部系统' : '调用工具');
}

export function toolTouchesBoard(t?: string) {
  return !!t && /consult_(add|propose)/.test(t);
}

// ---- plan actions --------------------------------------------------------------

const COMMANDS: Record<string, string> = {
  toollimit: '计划换模点',
  retool: '立即换模',
  start: '启动',
  stop: '停机',
  reset: '复位故障',
  maintenance: '切换保养',
  speed: '调整节拍',
  fault: '故障演练',
  restock: '补充原料',
  ship: '发运成品',
};

/** Arguments worth showing, labelled. Routing fields (company, plant ids) are left out. */
export function actionArgs(args?: Record<string, unknown>) {
  if (!args) return [];
  const out: { k: string; v: string }[] = [];
  const cmd = typeof args.command === 'string' ? args.command : '';
  if (args.asset != null) out.push({ k: '设备', v: String(args.asset) });
  if (args.line != null) out.push({ k: '产线', v: String(args.line) });
  if (cmd) out.push({ k: '指令', v: COMMANDS[cmd] ?? cmd });
  if (typeof args.arg === 'number') {
    const asShare = cmd === 'toollimit' || cmd === 'speed';
    out.push({ k: cmd === 'toollimit' ? '模具寿命' : '参数', v: asShare ? `${Math.round(args.arg * 100)}%` : String(args.arg) });
  }
  return out;
}

/** Why an action did not reach the site, in words; the raw reply stays one click away. */
export function actionFailure(err: string) {
  if (/connection refused|dial tcp|no such host|i\/o timeout|deadline exceeded|EOF/i.test(err)) return '连不上现场系统';
  if (/unauthori[sz]ed|forbidden|401|403/i.test(err)) return '现场系统拒绝了这个身份';
  if (/not allowed|not an action|unknown tool/i.test(err)) return '这个动作没有被允许执行';
  return '现场系统返回了错误';
}

/** What the far side said back, as a sentence. */
export function actionReply(result?: string) {
  if (!result) return '';
  try {
    const j = JSON.parse(result);
    if (typeof j === 'string') return j;
    return j.sent ?? j.msg ?? j.message ?? j.result ?? '已完成';
  } catch {
    return result.length > 160 ? `${result.slice(0, 160)}…` : result;
  }
}
