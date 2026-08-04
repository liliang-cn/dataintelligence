package modelgen

import (
	"slices"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"
)

// Curation — the three things a heuristic draft used to leave blank, and which
// are exactly the things that decide whether the draft is usable on site.
//
// The structural half of a draft (entities, joins, cardinality, grain) is
// derivable from the schema, and HeuristicModel gets it right. The governance
// half is not derivable, so it came out empty: `synonyms: []`, `mask: ""`,
// `roles: []`. Empty is not neutral. A model with no Chinese synonyms cannot be
// grounded by anyone asking in Chinese; a phone column with no mask is a phone
// column in every answer; a metric with no roles is revenue that everyone can
// read. Someone has to fill these in, and "someone will review it later" is how
// they stay empty.
//
// So the draft now guesses, and the guesses are biased. Which way to bias is not
// the same question for all three:
//
//   - Synonyms are purely additive. A wrong one costs a spurious match at worst.
//   - Masking and role-gating are the reverse of the bias used elsewhere in this
//     package (see looksLikeIdentifier: "a missing metric is invisible, a silly
//     one is not"). Here the invisible failure is the harmful one — an unmasked
//     phone number leaks quietly, while an over-masked column produces a
//     complaint within the hour and is deleted in one line. Default closed.
//
// Everything below is derived from the reviewed models running in production,
// not invented: the vocabulary comes from their synonym lists, and the
// money-vs-quantity split from which of their metrics carry `roles:`.

// cjkLexicon maps an English column/metric token to what a person actually
// types. Keys are matched against a whole name first, then against progressively
// shorter token suffixes, so `store_region` finds `region`.
var cjkLexicon = map[string][]string{
	// —— 钱 ——
	"revenue":   {"营收", "营业额", "销售额"},
	"sales":     {"销售", "销售额"},
	"amount":    {"金额"},
	"cost":      {"成本"},
	"cogs":      {"销售成本", "成本"},
	"profit":    {"利润"},
	"margin":    {"毛利"},
	"price":     {"价格", "单价"},
	"tax":       {"税费", "税"},
	"rent":      {"租金", "房租"},
	"salary":    {"工资", "薪酬"},
	"wage":      {"工资", "薪酬"},
	"spend":     {"支出", "花费"},
	"purchase":  {"进货", "采购额"},
	"discount":  {"折扣"},
	"refund":    {"退款"},
	"fee":       {"费用"},
	"utilities": {"水电", "能耗"},

	// —— 量 ——
	"qty":          {"数量"},
	"quantity":     {"数量"},
	"units":        {"件数", "销量"},
	"headcount":    {"人数", "员工数"},
	"transactions": {"交易数", "笔数"},
	"stock":        {"库存"},
	"inventory":    {"库存"},
	"loss":         {"损耗", "报损"},

	// —— 维度:地点与组织 ——
	"city":       {"城市"},
	"region":     {"区域", "地区", "大区"},
	"province":   {"省份", "地区"},
	"store":      {"门店", "店"},
	"shop":       {"门店", "店"},
	"clinic":     {"诊所", "门诊"},
	"hotel":      {"酒店", "门店"},
	"factory":    {"工厂", "厂"},
	"plant":      {"工厂", "基地"},
	"warehouse":  {"仓库", "仓"},
	"line":       {"产线", "生产线"},
	"dept":       {"部门"},
	"department": {"部门"},
	"channel":    {"渠道", "平台"},

	// —— 维度:人与物 ——
	"staff":    {"员工"},
	"employee": {"员工"},
	"customer": {"客户"},
	"member":   {"会员"},
	"guest":    {"客人", "顾客"},
	"patient":  {"患者", "病人"},
	"supplier": {"供应商"},
	"product":  {"产品", "商品"},
	"part":     {"零件", "配件"},
	"sku":      {"单品", "商品"},
	"order":    {"订单"},
	"room":     {"房间", "包间"},

	// —— 维度:属性 ——
	"category": {"品类", "类别", "类目"},
	"brand":    {"品牌", "牌子"},
	"status":   {"状态"},
	"type":     {"类型"},
	"tier":     {"等级"},
	"grade":    {"等级", "牌号"},
	"severity": {"严重度"},
	"position": {"岗位", "职位"},
	"role":     {"岗位", "职位"},
	"shift":    {"班次"},
	"format":   {"业态"},
	"defect":   {"缺陷", "不良"},
	"gender":   {"性别"},
	"age":      {"年龄"},

	// —— 时间 ——
	"date":    {"日期"},
	"time":    {"时间"},
	"month":   {"月份"},
	"year":    {"年份"},
	"quarter": {"季度"},
	"week":    {"周"},
}

// cjkSuffix turns a trailing token into a word that composes onto the base:
// order + count → 订单数. Only suffixes that read naturally in Chinese when
// glued directly to a noun are listed; anything needing a measure word is left
// out rather than produced wrong.
var cjkSuffix = map[string][]string{
	"count": {"数", "数量"},
	"sum":   {"合计"},
	"total": {"合计"},
	"rate":  {"率"},
	"ratio": {"占比"},
	"avg":   {"平均"},
}

// cjkSynonyms proposes Chinese synonyms for a generated dimension or metric
// name. Returns nil when nothing is recognized — an empty list is honest, a
// transliteration would not be.
func cjkSynonyms(name string) []string {
	toks := strings.Split(strings.ToLower(name), "_")
	if len(toks) == 0 {
		return nil
	}

	// Whole name first: `gross_margin` is its own word, not 毛利 composed.
	if v, ok := cjkLexicon[strings.Join(toks, "_")]; ok {
		return append([]string(nil), v...)
	}

	// A trailing count/sum/rate composes onto whatever it counts.
	if len(toks) > 1 {
		if tails, ok := cjkSuffix[toks[len(toks)-1]]; ok {
			base := cjkSynonyms(strings.Join(toks[:len(toks)-1], "_"))
			if len(base) == 0 {
				return nil
			}
			var out []string
			// `_sum` on a money column reads better bare: 订单金额, not 订单金额合计.
			if toks[len(toks)-1] == "sum" || toks[len(toks)-1] == "total" {
				out = append(out, base...)
			}
			for _, b := range base {
				for _, t := range tails {
					out = append(out, b+t)
				}
			}
			return dedupe(out, 4)
		}
	}

	// `X_per_Y` must not fall through to the suffix walk: order_amount_per_qty
	// would match on its *denominator* and come back 数量, which then competes
	// with order_qty_sum for that exact word. Two metrics answering to 数量 is
	// worse than one metric with no Chinese name — the first silently returns
	// the wrong number, the second visibly returns none. Whole-name entries
	// (客单价, 人均营收) are already handled above.
	if len(toks) > 2 && slices.Contains(toks[1:len(toks)-1], "per") {
		return nil
	}

	// Otherwise the attribute carries the meaning: store_region → 区域, not 门店区域.
	// Walk from the longest suffix so `id_card` beats `card`.
	for i := range toks {
		if v, ok := cjkLexicon[strings.Join(toks[i:], "_")]; ok {
			return append([]string(nil), v...)
		}
	}
	return nil
}

// piiColumn reports whether a column holds personal data that should not appear
// verbatim in an answer.
//
// Person names are deliberately absent. `customer_name` is PII and `store_name`
// is not, and nothing in a schema distinguishes them; masking both would break
// grouping by store — the single most common thing anyone asks for — to protect
// a column the reviewer can mask in one line. The reviewed models mask phone
// numbers and nothing else, which is the same conclusion reached by hand.
func piiColumn(column string) bool {
	n := strings.ToLower(column)
	for _, frag := range []string{
		"phone", "mobile", "telephone", "email", "e_mail",
		"id_card", "idcard", "id_no", "idno", "identity_no", "ssn", "passport",
		"bank_card", "bankcard", "card_no", "cardno", "account_no", "accountno",
		"address", "addr", "postcode", "zipcode",
	} {
		if strings.Contains(n, frag) {
			return true
		}
	}
	// A bare `tel` column, but not `hotel` / `telecom`.
	return n == "tel" || strings.HasSuffix(n, "_tel")
}

// maskExpr is what a masked dimension renders as. A literal, so the column never
// reaches the result set at all.
const maskExpr = `'***'`

// moneyMetric reports whether a metric measures money, and therefore should be
// gated to finance by default.
//
// Rates are split, not lumped: `margin_rate` and `net_margin` are gated in every
// reviewed model because they expose margin, while `yield_rate`, `defect_rate`
// and `on_time_rate` are open in all of them. So the test is the measure, not
// the suffix — a ratio built on money is money.
func moneyMetric(name, expr string) bool {
	hay := strings.ToLower(name + "_" + expr)
	for _, frag := range []string{
		"revenue", "sales", "amount", "price", "cost", "cogs", "profit", "margin",
		"salary", "wage", "payroll", "spend", "payment", "fee", "tax", "rent",
		"gmv", "income", "expense", "budget", "discount", "refund", "deposit",
		"adr", "revpar", "arpu", "aov", "ticket", "turnover",
	} {
		if strings.Contains(hay, frag) {
			return true
		}
	}
	return false
}

// curate fills the governance blanks in a structurally-complete draft: Chinese
// synonyms so the thing can be asked about in Chinese, masks on personal data,
// and a finance gate on money.
//
// It only ever fills blanks. Anything already set — by a hand edit, or by the
// LLM refinement pass — is left alone, so re-running the draft never quietly
// widens a gate somebody narrowed. `admin` rides along on the finance gate so
// whoever administers the deployment is not locked out of their own data.
func curate(m *semantic.Model) {
	for i := range m.Dimensions {
		d := &m.Dimensions[i]
		if len(d.Synonyms) == 0 {
			d.Synonyms = cjkSynonyms(d.Name)
		}
		// A time dimension is asked about as 时间 whatever the column is called.
		// created_at, order_date, biz_dt, ts — enumerating the spellings is a
		// losing game, and the type already settled the question.
		if len(d.Synonyms) == 0 && d.Type == "time" {
			d.Synonyms = []string{"时间", "日期"}
		}
		if d.Mask == "" && piiColumn(d.Column) {
			d.Mask = maskExpr
		}
	}
	for i := range m.Metrics {
		mt := &m.Metrics[i]
		mt.Synonyms = dedupe(append(mt.Synonyms, cjkSynonyms(mt.Name)...), 8)
		if len(mt.Roles) == 0 && moneyMetric(mt.Name, mt.Expr+" "+mt.Formula) {
			mt.Roles = []string{"finance", "admin"}
		}
	}
}

func dedupe(in []string, max int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == max {
			break
		}
	}
	return out
}
