# FDE 路线图 —— 把这套东西变成前置部署工程师的基础框架

**目标**:FDE 用这套系统开展工作、交付产品和系统。不是「一个能查数的工具」,
而是**一次交付从进场到移交的全部脚手架**。

本文档是计划,不是承诺。每一条都写清楚:做什么、为什么、动哪里、怎么算做完。

**状态(2026-08-03)**:17 项全部落地。下面保留了每一项当初的理由,并在标题上标了
完成状态——理由比清单有用,因为下一次做类似判断时要复用的是理由。

---

## 一、这个框架管什么,不管什么

FDE 的定义性特征(Wikipedia):交付**生产代码**、对**落地结果**负责、
并把**反复出现的需求回传给核心产品团队**。前两条区别于 solutions architect,
第三条区别于咨询。

[Awesome-FDE-Roadmap](https://github.com/pierpaolo28/Awesome-FDE-Roadmap) 把 FDE
的技能拆成三段:数据工程 / 云架构 / 咨询心法。其中云架构那一段(GKE、Terraform、
VPC Service Controls、BigQuery 调优)是**FDE 本人的技能树**,不是框架该长的东西
——硬做就变成又一个云平台。

**这个框架只管中间那条脊椎:**

```
进场  →  勘察  →  建模  →  验证  →  交付  →  移交  →  回流
        ↑                                              ↓
        └────────── 下一次交付更便宜 ──────────────────┘
```

最后那条回流箭头是重点。咨询做完就走;FDE 让第 N 次比第 1 次便宜。
没有它,这只是一个交付工具,不是 FDE 框架。

---

## 二、核心抽象:engagement

现在系统里的单位是「一个数据库」。对 FDE,单位是**一次交付**:一个客户、
若干个库、一份语义模型、一份对账集、一份评测集、一份报告、一条审计链、
以及**这次自己补的那些东西**。

```yaml
# engagement.yaml —— 一次交付的全部声明
customer: 客户A
started: 2026-07-29
owner: liliang

databases:
  - id: erp
    dsn: ${ERP_DSN}                  # 只读账号
    model: models/erp.yaml
    recon: models/erp.recon.yaml
  - id: pos
    dsn: ${POS_DSN}                  # 还没建模,直连模式

evalset: models/questions.yaml
deliver:
  report: out/delivery.md
  roles: [ceo, finance, store_manager]

# 本次为了让 core 够用而临时补的东西。汇总多次交付后,
# 反复出现的那几项就是下一个该进 core 的功能。
delta:
  - kind: transform
    what: ERP 的日期存成 varchar 'YYYYMMDD',落库前要转
    workaround: examples/ingest-recipes/transform_erp_dates.sql
  - kind: missing-feature
    what: 客户要按财年(4 月起)聚合,semantic-go 只有自然年
```

有了它,现有的四十多个子命令从「各自带一堆 flag」变成「对这次交付做某件事」。
**这是把工具变成框架的那一步**,也是后面每一件事的挂载点。

---

## 三、七个阶段:现状与缺口

已有的能力比看起来多。下表左边全是**已经能跑的东西**,缺口只在右边。

| 阶段 | 已有 | 缺口 |
|---|---|---|
| **0 进场** | 5 种引擎(PG/MySQL/SQLite/SQL Server/DuckDB)· 运行时注册 · 保存前先真连 | 接入说明:连了什么、能看到什么、**看不到什么** |
| **1 勘察** | `/v1/sql` · `/v1/tables` · `modelgen.Introspect` | **`di survey`** —— 第一周唯一的交付物,现在完全没有 |
| **2 建模** | `di model gen` · `di model lint` · `POST /v1/databases/{id}/model` | 草稿有噪音(实测生成 `order_store_id_sum` 这类外键求和) |
| **3 验证** | `di eval`(对账)· `di nleval`(NL 准确率)· `di report`(交付报告) | **对照查询的出处**;两个集子都得手写 |
| **4 交付** | di-server(直连/治理双模式)· 桌面版 · `di report` | 产品层没有用户体系(core 有 RBAC/RLS,产品只有单令牌) |
| **5 移交** | `rollout`(版本注册/灰度/自动回滚)· `shadow`(跨版本差异)· `threats` · `pentest` | 没绑到 engagement · 没有 drift 告警 · 没有运维手册 |
| **6 回流** | —— | **全部**。delta 没有任何记录 |

---

## 四、分期

### P0 —— 一次交付端到端可复现 ✅

**完成标志**:从零到一份能递给 CFO 的报告,别人照着 engagement.yaml 能复现。

| # | 做什么 | 为什么 |
|---|---|---|
| 1 | `engagement.yaml` + 所有命令接受 `-engagement` | 后面每一件事的挂载点 |
| 2 | **`di survey`** —— 表清单、行数、真实时间范围、空值率、疑似枚举、孤儿外键 | 对应 roadmap 的 **Site Survey**。第一周的产出,而且材料现成 |
| 3 | recon 加 `source:` 字段 | 见下方「那个洞」 |
| 4 | `di model gen` 不再把外键/主键生成为指标 | 递给客户的第一份东西不该有明显噪音 |
| 5 | 产品接 `@ai-gui/plugin-resultset` | 数字不再经模型的手重打一遍 |

#### P0 里最重要的一条:那个洞

`di eval` 的逻辑是「指标 vs 手写对照查询,必须相等」。但**如果两条都是同一个人
写的、出自同一个误解,它们会一致——一致不等于对**。

真正的对照应该来自**客户已有的那个数**:财务表里的营收、月报上的订单量。
所以 recon 用例要记出处,报告要分开显示:

```yaml
cases:
  - metric: net_revenue
    control: SELECT ...
    source: customer-report      # 客户 2026Q2 财报,第 3 页
    note: 营收扣退款。客户口径含税
  - metric: order_qty_sum
    control: SELECT sum(qty) FROM orders
    source: engineer             # 我看着表结构推的,没有外部锚点
```

报告里就能写:「6 条对账中,4 条锚定客户既有数字,2 条为工程师自行推导」。
**那句话才值钱**——它把「验证」和「验证剧场」分开了。

### P0.5 —— Day 2 ✅

被 invisibletech 点名为头号失败模式:
> "client teams lack capacity to **maintain delivered solutions afterward**"

东西交出去没人维护得住,前面全白做。所以移交**不能排在最后**。

| # | 做什么 | 为什么 |
|---|---|---|
| 6 | **`di handover`** —— 生成运维手册:怎么加指标、怎么跑闸门、坏了看哪里、谁负责 | 术语表里的 "Day 2 Operations" |
| 7 | 把 `di eval` 装进客户的 CI(生成 workflow 文件) | 口径漂移当场就断,而不是三个月后有人发现数不对 |
| 8 | **drift 告警** —— 定期跑对账,数据变了/模型没跟上就报警 | roadmap 的 outer loop(生产侧监控) |

`rollout` 和 `shadow` 已经存在,这一期主要是**把它们绑到 engagement 上**并生成文档。

### P1 —— 让证明可信,让产品能给多人用 ✅

| # | 做什么 | 为什么 |
|---|---|---|
| 9 | **`di anchor`** —— 给一个客户已发布的数字,反推它对应哪个范围 | 把「我和我自己一致」变成「我和客户的账一致」。这是 P0 那个洞的真正解法 |
| 10 | **`di questions`** —— 从审计里挖真实提问,归并同义问法,按人数排序 | 手写评测集测的是写的人的想象力 |
| 11 | 产品多用户 + 角色 | 客户有 5 个高管,「谁看了什么」在产品层要答得上来 |
| 12 | **`di adoption`** —— 谁在问、多久问一次、**哪些指标从来没人查** | 对应 **Weekly Executive Summary**。`di report` 证明算得对,但没有任何东西显示有人在用。一个没人打开的正确看板等于零 |

### P1.5 —— Delta 回流 ✅

**这是 FDE 与咨询的分水岭,也是我第一版规划完全漏掉的一块。**

| # | 做什么 | 为什么 |
|---|---|---|
| 13 | `engagement.yaml` 的 `delta:` 段落规范 | 记录本次为了让 core 够用而补的东西 |
| 14 | **`di delta`** —— 跨多个 engagement 汇总,按出现次数排序 | 重复出现的那几项,就是下一个该进 core 的功能 |

举例:三个客户都要「财年从 4 月起」,那就不该是第四次再写一遍绕过方案,
而是 semantic-go 该支持 `fiscal_year_start`。

**没有这一步,飞轮不转**,每次交付的成本都一样。

已经转过两轮:一汽那个库让 `modelgen` 暴露出「比率生成器只认零售的收入/成本形状」,
SAP 那条链让落地路径暴露出六个洞(整数除法、补零码、列名大小写、无主键、
`sum(MANDT)`、逐行 INSERT)。都是先记进 `delta:`,再变成 core 的功能。

### P2 —— 规模化 ✅

| # | 做什么 |
|---|---|
| 15 | engagement 级别的审计隔离(一个部署服务多个客户) |
| 16 | 交付包 —— 一次 engagement 打包成可移交的整体 |
| 17 | `examples/` 变成行业起手式库(现有 meridian / fitness 是雏形) |

15 的做法:`_audit` 加 `engagement` 列,`config.yaml` 的 `engagement:` 往每一行上盖戳,
读审计的命令(`adoption` / `questions`)一律按它过滤。**不能过滤到某个客户的审计,
就不能拿给那个客户看**——那一刻会有人把整张表导出去,连带别人的提问一起交出去。

16 的做法:`di package` 打 tar.gz,附一份 MANIFEST:每个文件的 SHA-256、
**缺哪些交付物**(缺就退出码非零)、需要哪些环境变量(只写名字,不写值)、
以及 di 和 semantic-go 的版本。最后一条最容易被忽略:编译器版本决定每个指标背后的
SQL,两个版本可以都对但末位不一致,而「这个数变了」是必须能回答的。

---

## 五、明确不做

| 不做 | 为什么 |
|---|---|
| 通用 BI 看板 | 那是往产品走不是往框架走,而且是红海 |
| 「模型提议 SQL、人点执行」 | 与治理门控直接冲突。要做是另一个产品(给数据团队,不是给老板) |
| 云基础设施层(Terraform/K8s/Spark) | 那是 FDE 本人的技能树。硬做变成又一个云平台 |
| ~~数据质量闸门~~ | **这条判断错了**。roadmap Phase 1 的 "data quality & observability" 不属于技能树而属于框架:`StageWithKey` 在落地时验证声明的主键(「你说 VBELN+POSNR 唯一标识一行,它不是」)就是 Bronze 层闸门,`examples/erp` 是样板 |
| 课程 / 培训 | 工具复利,课程不复利 |
| 纯事务型系统(下单、支付) | 和语义层没关系,别硬套 |

---

## 六、关于「交付系统」而不只是「交付报表」

只读的部分回答「看得对」。要交付**系统**,门在 `writeback`
(提议 → 审批 → 提交 → 回滚)——它已经在代码里,只是没进这条故事线。

同一套语义模型、同一套 RBAC、同一条审计链,从「读」延伸到「写」。这是从
交报表到交系统的唯一一道门,而且已经开了一半。P2 之后再正式并入。

---

## 七、四份 FDE 标准交付物的对应关系

roadmap 定义了四份模板。对照现状:

| FDE 标准交付物 | 这里的对应 | 状态 |
|---|---|---|
| Site Survey | `di survey` | ✅ 另长出了分段停摆检查和隐式外键检查 |
| Technical Scoping & PRD | `engagement.yaml` + 语义模型 | ✅ |
| Deployment Architecture | `docs/DEPLOYMENT.md` | ✅ |
| Weekly Executive Summary | `di report` + `di adoption` | ✅ |

---

## 一次交付的完整命令序列

```bash
di survey     -engagement engagement.yaml -out out/survey.md   # ① 先看,别问
di model gen  -dsn "$DSN" -out models/x.yaml                   # ② 草稿,然后人改
di anchor     -metric 一次合格率 -value 97.3 -note "月报第4页"   # ③ 用客户的数定口径
di eval       -engagement engagement.yaml                       # ④ 每个指标对账
di questions  -engagement engagement.yaml -out out/mined.yaml   # ⑤ 从真实提问挖评测集
di report     -engagement engagement.yaml                       # ⑥ 验收报告
di handover   -engagement engagement.yaml                       # ⑦ 运维手册 + CI 闸门
di drift      -engagement engagement.yaml                       # ⑧ 之后每天跑
di adoption   -engagement engagement.yaml                       # ⑨ 有没有人在用
di package    -engagement engagement.yaml                       # ⑩ 打包移交
di delta      -root ~/engagements                               # ⑪ 跨客户汇总,回流产品
```

③ 是分水岭:没有它,验收报告最多写「自洽」;有它,才写得出「已验证」。
⑪ 是 FDE 和咨询的分水岭:没有它,第 N 次交付和第 1 次一样贵。
