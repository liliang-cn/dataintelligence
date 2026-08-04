package modelgen

import (
	"strings"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"
)

// `has` lives in generate_test.go.

func TestCJKSynonymsComeFromTheAttribute(t *testing.T) {
	// store_region is asked about as 区域, not as 门店区域 — the entity prefix is
	// there to make the name unique, not because anyone says it out loud.
	for _, tc := range []struct {
		name string
		want string
	}{
		{"store_region", "区域"},
		{"customer_city", "城市"},
		{"product_category", "品类"},
		{"staff_position", "岗位"},
	} {
		if got := cjkSynonyms(tc.name); !has(got, tc.want) {
			t.Errorf("cjkSynonyms(%q) = %v, 少了 %q", tc.name, got, tc.want)
		}
	}
}

func TestCJKSynonymsComposeCountsAndRates(t *testing.T) {
	if got := cjkSynonyms("order_count"); !has(got, "订单数") {
		t.Errorf("order_count = %v, 想要 订单数", got)
	}
	if got := cjkSynonyms("customer_count"); !has(got, "客户数") {
		t.Errorf("customer_count = %v, 想要 客户数", got)
	}
	// A sum of money reads bare: 金额, not 金额合计.
	if got := cjkSynonyms("order_amount_sum"); !has(got, "金额") {
		t.Errorf("order_amount_sum = %v, 想要 金额", got)
	}
	// A whole-name entry wins over composing its parts.
	if got := cjkSynonyms("revenue"); !has(got, "营收") {
		t.Errorf("revenue = %v, 想要 营收", got)
	}
}

func TestCJKSynonymsStaySilentWhenUnsure(t *testing.T) {
	// Better an empty list than a transliteration nobody would ever type.
	for _, name := range []string{"widget_frobnicator", "xyz_abc", "foo"} {
		if got := cjkSynonyms(name); len(got) != 0 {
			t.Errorf("cjkSynonyms(%q) = %v, 不认识的词就该沉默", name, got)
		}
	}
}

func TestPerUnitMetricsDoNotStealTheirDenominatorsWord(t *testing.T) {
	// order_amount_per_qty used to come back 数量 — matched on the denominator —
	// and then competed with order_qty_sum for that word. Two metrics answering
	// to 数量 means one of them silently returns the wrong number.
	for _, name := range []string{"order_amount_per_qty", "sale_revenue_per_order", "revenue_per_head"} {
		if got := cjkSynonyms(name); len(got) != 0 {
			t.Errorf("cjkSynonyms(%q) = %v, 单位比率不该借用分母的词", name, got)
		}
	}
	// The denominator itself keeps its word.
	if got := cjkSynonyms("order_qty_sum"); !has(got, "数量") {
		t.Errorf("order_qty_sum = %v, 想要 数量", got)
	}
}

func TestPIIMasksContactColumnsButNotStoreNames(t *testing.T) {
	for _, col := range []string{"phone", "member_phone", "guest_mobile", "email", "id_card", "bank_card_no", "address", "tel"} {
		if !piiColumn(col) {
			t.Errorf("%q 该被认成 PII", col)
		}
	}
	// Masking every name column would break grouping by store — the single most
	// common thing anyone asks for — to protect a column a reviewer masks in one
	// line. And `hotel` must not trip the bare-`tel` rule.
	for _, col := range []string{"customer_name", "store_name", "hotel", "hotel_name", "telecom_provider", "category"} {
		if piiColumn(col) {
			t.Errorf("%q 不该被当成 PII", col)
		}
	}
}

func TestMoneyGatesRatiosBuiltOnMoneyButNotOperationalRates(t *testing.T) {
	// Every reviewed model gates margin_rate and net_margin, and leaves
	// yield_rate / defect_rate / on_time_rate open. The test is the measure,
	// not the `_rate` suffix.
	for _, m := range []string{"revenue", "sale_gross_margin", "margin_rate", "net_profit", "rd_spend", "avg_ticket", "adr", "revpar", "order_amount_sum"} {
		if !moneyMetric(m, "") {
			t.Errorf("%q 是钱,该门禁", m)
		}
	}
	for _, m := range []string{"yield_rate", "defect_rate", "on_time_rate", "units_sold", "stock_on_hand", "occupancy", "produced_qty", "items_per_order"} {
		if moneyMetric(m, "") {
			t.Errorf("%q 不是钱,不该门禁", m)
		}
	}
	// The expression counts too: a metric named `total` over an `amount` column
	// is still money.
	if !moneyMetric("order_total", "amount") {
		t.Error("表达式里的 amount 也该算钱")
	}
}

func TestCurateFillsBlanksAndNeverWidensWhatSomeoneNarrowed(t *testing.T) {
	m := &semantic.Model{
		Dimensions: []semantic.Dimension{
			{Name: "customer_phone", Entity: "customer", Column: "phone", Type: "categorical"},
			{Name: "store_region", Entity: "store", Column: "region", Type: "categorical"},
			// Already curated by hand — must survive untouched.
			{Name: "member_phone", Entity: "member", Column: "phone", Type: "categorical",
				Synonyms: []string{"手机"}, Mask: "'hidden'"},
		},
		Metrics: []semantic.Metric{
			{Name: "sale_amount_sum", Entity: "sale", Agg: "sum", Expr: "amount"},
			{Name: "sale_qty_sum", Entity: "sale", Agg: "sum", Expr: "qty"},
			// A hand-narrowed gate must not be replaced by the default one.
			{Name: "sale_cost_sum", Entity: "sale", Agg: "sum", Expr: "cost", Roles: []string{"cfo"}},
		},
	}
	curate(m)

	if m.Dimensions[0].Mask != maskExpr {
		t.Errorf("phone 没被脱敏: %q", m.Dimensions[0].Mask)
	}
	if !has(m.Dimensions[1].Synonyms, "区域") {
		t.Errorf("store_region 没拿到中文同义词: %v", m.Dimensions[1].Synonyms)
	}
	if m.Dimensions[2].Mask != "'hidden'" || !has(m.Dimensions[2].Synonyms, "手机") {
		t.Error("手工写好的脱敏与同义词被覆盖了")
	}
	if !has(m.Metrics[0].Roles, "finance") {
		t.Errorf("金额指标没上门禁: %v", m.Metrics[0].Roles)
	}
	if len(m.Metrics[1].Roles) != 0 {
		t.Errorf("数量指标不该门禁: %v", m.Metrics[1].Roles)
	}
	if len(m.Metrics[2].Roles) != 1 || m.Metrics[2].Roles[0] != "cfo" {
		t.Errorf("手工收紧过的门禁被放宽了: %v", m.Metrics[2].Roles)
	}
}

// The whole point is that a draft off a live schema is usable in Chinese and
// safe by default — not that the helpers pass in isolation.
func TestDraftFromSchemaIsGroundableInChineseAndSafeByDefault(t *testing.T) {
	schema := &Schema{Tables: []Table{
		{
			Name: "customers", PrimaryKey: "id",
			Columns: []Column{
				{Name: "id", Type: "integer"},
				{Name: "name", Type: "text"},
				{Name: "phone", Type: "text"},
				{Name: "city", Type: "text"},
			},
		},
		{
			Name: "orders", PrimaryKey: "id",
			Columns: []Column{
				{Name: "id", Type: "integer"},
				{Name: "customer_id", Type: "integer"},
				{Name: "amount", Type: "numeric"},
				{Name: "created_at", Type: "timestamp without time zone"},
			},
			ForeignKeys: []ForeignKey{{Column: "customer_id", RefTable: "customers", RefColumn: "id"}},
		},
	}}

	m, err := HeuristicModel(schema)
	if err != nil {
		t.Fatal(err)
	}

	var phone, city *semantic.Dimension
	for i := range m.Dimensions {
		switch m.Dimensions[i].Column {
		case "phone":
			phone = &m.Dimensions[i]
		case "city":
			city = &m.Dimensions[i]
		}
	}
	if phone == nil || phone.Mask == "" {
		t.Error("手机号列起草出来时没有脱敏 —— 泄漏是看不见的,过度脱敏是一行就能删的")
	}
	if city == nil || !has(city.Synonyms, "城市") {
		t.Error("城市维度没有中文同义词,中文提问 ground 不上")
	}
	// created_at 不在词表里,靠 type=time 兜底 —— 时间列的拼法穷举不完。
	for _, d := range m.Dimensions {
		if d.Column == "created_at" && !has(d.Synonyms, "时间") {
			t.Errorf("时间维度没有中文同义词: %v", d.Synonyms)
		}
	}

	var gatedMoney, openQty bool
	for _, mt := range m.Metrics {
		if strings.Contains(mt.Name, "amount") && has(mt.Roles, "finance") {
			gatedMoney = true
		}
		if strings.HasSuffix(mt.Name, "_count") && len(mt.Roles) == 0 {
			openQty = true
		}
	}
	if !gatedMoney {
		t.Error("金额指标默认没上财务门禁")
	}
	if !openQty {
		t.Error("计数指标被误门禁了,数量本该人人可见")
	}
}
