# 产品外壳参考：Inception Data Engine 拆解

来源：<https://v0-data-engine-design.vercel.app/>（v0 生成的企业数据引擎 UI 原型，"Inception Data Engine"）。

**为什么记这份**：DI 的内核（治理语义层 / fan-out·chasm-safe 编译 / RBAC·RLS·mask / writeback / connectors）已经比这个原型硬。我们缺的是**把内核变成"老板/实施顾问能用的产品"的那层外壳**。这个原型不提供技术，但它把"一个数据引擎产品该长什么样"的**信息架构（IA）**摆得很清楚，值得抄骨架、跳过空心。

一句话定位：**我们内核硬、壳薄；它壳漂亮、底下空。抄它的骨架，别抄它的大而全。**

---

## 1. 它的信息架构（全站导航）

左侧分组导航 + 右侧常驻 Copilot（`Context: 当前区块`）。

| 分组 | 区块 | 状态 |
|---|---|---|
| **View** | Dashboards / Data Visualization / Data Explorer | 做实 |
| **Run** | Workflow Runs | 做实 |
| **Build** | Data Sources / Transforms / Workflows | 做实 |
| **System** | Audit & Review Center / Monitoring / Settings | 前者做实，后两者是占位（under development） |

顶栏：租户切换（多租户 `Tenant: Acme Corp`）、全局 `Ask AI…（⌘K）`、通知/用户。

---

## 2. 逐页要点，及对应我们已有的件

| 页面 | 它做了什么 | DI/harness 已有的对应件 |
|---|---|---|
| **Dashboards** | "描述一句话→AI 搭看板" + 手动挂件（Greeting / AI Insight / Metrics / Recent Tasks / Charts） | aigui + harness agent |
| **Data Visualization** | 选 `Data Node` → 自然语言生成图表（"按品类比营收"）；History / Saved | `query_metric` + aigui 渲染 |
| **Data Explorer** | 自然语言→AI 写查询 + 类电子表格网格；**敏感列打 `?` 标记**；Related Data Sources 计数；Export | DI 治理查询 + PII mask（已做） |
| **Workflow Runs** | 执行历史：逐步骤成败、耗时、触发者（Scheduler / 事件 / 人）、成功率 KPI | harness hash-chain 审计链（我们的更强） |
| **Data Sources** | **粘连接串/一句话描述→AI 接数据源**；**AI 连接体检**（"MongoDB 连不上，多半凭证过期或 IP 白名单"→Fix）；每源挂 `N nodes`；Test/Edit | **DI connectors：csv/xlsx/mssql/pg/mysql/mongo/redis/s3/kafka** |
| **Transforms** | 源节点→目标节点 ETL 流；Schedule（每小时/实时/每日）；**版本号 v1,456**；成功率 | DI ingest / 字段映射 |
| **Workflows** | 把多个 transform 编排成一个流；触发 Schedule / Event-driven / Manual | 我们偏 agent 编排，不完全对应 |
| **Audit & Review Center** | **Sandbox→Prod 晋级审批队列**；变更 diff `+3421 ~567 -89`（增/改/删，像 git）；申请人+部门+优先级；**AI 风险分诊**（"这条动了生产数据、改了字段映射，先审它"） | **DI writeback：propose / approve / reject / revert** |
| **Monitoring / Settings** | 占位，没做 | — |

---

## 3. 真正该抄的 6 个模式（都能落到现有件上）

### 3.1 每页统一骨架（最值钱）
每一页都是同一个模板：

```
标题 + 副标题
  → AI 动作条（"描述它 → Generate/Build"，带 "Try asking" 示例芯片）
  → KPI 磁贴（Total / Active / Failed / Rate…）
  → 过滤行（搜索 + 下拉）
  → 表格 / 数据网格
  → 分页
```

**动作**：给 aigui 定一个这样的页面模板，10 个行业垂直全套用。这是把内核标准化成产品的那层壳。

### 3.2 AI 是"每页一个入口"，不是一个万能聊天框
每页有本页专属的自然语言动作（搭看板 / 生成图 / 写查询 / 接数据源 / 连接体检）。比一个 bolt-on chatbot 强。
**动作**：harness agent 绑定"当前区块上下文"——在"药店动销率"页提问自动带 Context。

### 3.3 `Data Node` 抽象
数据源 → N 个 Node；所有查询/图/transform 都指向一个 Node。这就是 DI 的 entity/model。
**动作**：UI 上显式化成"数据节点选择器"，用户永远在治理过的节点上操作，天然挡住裸 SQL。

### 3.4 Data Sources 页 = 离钱最近的一块
"粘连接串/一句话→AI 帮你接" + "AI 连接体检"，正好把已做的 DI connectors 包成老板/顾问能用的界面。
**动作**：蹲厂两周里最花时间的"接数据"，就靠这一页产品化。

### 3.5 Audit & Review Center = DI writeback 的现成门面
晋级审批队列 + `+增 ~改 -删` diff + Sandbox→Prod + AI 风险分诊 ≈ DI `writeback propose/approve/reject/revert` 的 UI。后端已有，只差露出来。
**动作**：把 hash-chain 访问审计也并进这个中心（它这块反而没做，我们更强）。

### 3.6 敏感列打标 `?`
Data Explorer 对疑似敏感/待分类列打 `?`。
**动作**：schema 探测时自动标出疑似 PII 列，让顾问一眼确认是否 `mask:`。

---

## 4. 别被带偏（它有而我们不该学）

- **它是纯 UI 壳，底下没引擎**：没有 chasm-safe 编译、没有真 RBAC/RLS/mask 落地、Monitoring/Settings 直接占位。我们反过来——内核比它硬，壳比它薄。
- **别学"大而全"**。它 11 个区块面向大厂（Snowflake/BigQuery/Kafka）。我们的客户是中小企业老板，**三页够**：接数据 → 问数/看板 → 审计&审批。多了就是负担。
- **它没证明"能跑对"**——我们有实测（口腔椅位利用率 DI 41.64% vs 裸 fan-out 2.03%，错 ~20×；酒店入住率 59.1% vs 5.1%；汽修工时利用率 60.8% vs 5.6%；药店动销率 0.122 vs 0.016）。正确性是我们的护城河，不是它的。

---

## 5. 建议：最小三页 IA（把内核变产品）

| 页 | 干什么 | 对接后端 | 角色 |
|---|---|---|---|
| **① 接数据** | AI 接数据源 + schema 探测 + 疑似 PII 标记 + Test | DI connectors + ingest；`describe_warehouse` | admin |
| **② 问数 / 看板** | 选 Data Node → 自然语言问数/生成图/搭看板 | MCP `list_metrics` / `get_dimensions` / `query_metric`（无 run_sql） | finance / analyst（RBAC 决定能看哪些指标） |
| **③ 审计 & 审批** | 访问审计（hash-chain）+ 变更晋级审批（writeback，Sandbox→Prod，+/~/- diff，AI 风险分诊） | harness AuditHook + DI `writeback` | admin |

外壳交给 aigui；内核就是现在的 DI + harness + 10 个行业垂直。
