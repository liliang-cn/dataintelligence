package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A model asked to summarise twenty tool results will, now and then, name a supplier or a machine
// that none of them returned, or round a number into one nobody measured. The findings and plans
// it records are safe (the server runs their evidence); the prose answer is not. So before the
// answer goes out, it is checked against everything the tools actually returned:
//
//  1. a second pass of the model rewrites the answer keeping only what the results support (or
//     what plainly follows from them by arithmetic), and lists what it took out;
//  2. identifiers shaped like equipment or order codes (HP-501, MO-261006-10601) that still do
//     not appear in any result are listed under the answer, because a code is either in the data
//     or invented — there is no arithmetic that produces one.

// evidence is what one run's tools returned, as text the checker can read.
type evidence struct {
	b strings.Builder
}

// The check has to see the end of the run as well as the start: that is where the simulations
// and the recorded goal, findings and plan are. So no single result may crowd out the rest, and
// the catalogue tools (what metrics and dimensions exist) are left out: they hold no facts.
const (
	evidenceLimit = 600_000
	resultLimit   = 8_000
)

var catalogue = map[string]bool{"list_metrics": true, "get_dimensions": true, "describe_warehouse": true, "consult_list": true}

func (e *evidence) add(tool string, args map[string]any, result any) {
	if catalogue[tool] || e.b.Len() > evidenceLimit {
		return
	}
	a, _ := json.Marshal(args)
	r, err := json.Marshal(result)
	if err != nil {
		r = []byte(fmt.Sprint(result))
	}
	if len(r) > resultLimit {
		r = append(r[:resultLimit:resultLimit], []byte("…(截断)")...)
	}
	fmt.Fprintf(&e.b, "### %s %s\n%s\n\n", tool, a, r)
}

func (e *evidence) String() string {
	s := e.b.String()
	if len(s) > evidenceLimit {
		s = s[:evidenceLimit]
	}
	return s
}

var codeRe = regexp.MustCompile(`\b[A-Z]{1,5}-\d{2,6}(?:-\d{2,6})*\b`)

// unknownCodes are the code-shaped identifiers in answer that no tool result contains.
func unknownCodes(answer, ev string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range codeRe.FindAllString(answer, -1) {
		if seen[c] || strings.Contains(ev, c) {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

const verifyPrompt = `你是审稿人。下面是一位顾问本轮调用工具拿到的全部原始结果，以及他写给客户的回答。

规则：
- 回答里出现的每个名称（设备编号、产线、钢厂、炉号、客户、缺陷类型等）都必须在工具结果里出现过。
- 每个数字都必须在工具结果里出现过，或者能由结果里的数字直接算出（求和、差、占比、换算百分比、合理四舍五入）。
- 找出所有不满足的地方：名称换成结果里真实的那个（能确定时），否则删掉那句话；数字改成结果里的值，算不出来就删掉。
- consult_add_goal / consult_add_finding / consult_propose_plan 的结果说明顾问确实记下了目标、结论和计划，回答里提到它们（编号、内容）是有依据的。
- 工具结果里标了（截断）的部分你看不到，不要因为看不到就删除，只删除和你看得到的结果相矛盾、或者明显是编造的名称和数字。
- 不要新增结论，不要改变结构、语气和格式（保留 Markdown），没有问题就原样返回。

只输出 JSON：{"revised": "<改好的完整回答>", "removed": ["<被删除或改正的说法，简短>", ...]}

=== 工具结果 ===
%s

=== 顾问的回答 ===
%s`

// verify returns the answer checked against the evidence and what was changed.
func verify(ctx context.Context, llm domain.Generator, answer string, ev *evidence) (string, []string) {
	text := ev.String()
	if strings.TrimSpace(answer) == "" || text == "" || llm == nil {
		return answer, nil
	}
	out, err := llm.Generate(ctx, fmt.Sprintf(verifyPrompt, text, answer), &domain.GenerationOptions{Temperature: 0})
	var got struct {
		Revised string   `json:"revised"`
		Removed []string `json:"removed"`
	}
	if err == nil {
		raw := strings.TrimSpace(out)
		raw = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(raw, "```json"), "```"), "```")
		if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j > i {
			raw = raw[i : j+1]
		}
		if json.Unmarshal([]byte(raw), &got) == nil && strings.TrimSpace(got.Revised) != "" {
			answer = got.Revised
		}
	}
	changes := got.Removed
	if codes := unknownCodes(answer, text); len(codes) > 0 {
		answer += "\n\n> 以下编号没有出现在本轮任何查询结果里，请勿采信：" + strings.Join(codes, "、")
		changes = append(changes, "未经查询的编号："+strings.Join(codes, "、"))
	}
	if err != nil {
		changes = append(changes, "核对没有完成："+err.Error())
	}
	return answer, changes
}
