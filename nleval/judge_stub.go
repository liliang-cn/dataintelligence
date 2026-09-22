//go:build !engineer

package nleval

import (
	"context"
	"io"
)

// JudgeReport 在默认构建里是个空壳。
//
// 真正的实现走 eval-go 的 llmjudge,而它会把一整个 agent 框架拽进依赖图。
// 打分这件事发生在工程师改模型的时候,不发生在客户的服务器上——所以它在
// `-tags engineer` 后面,交付出去的二进制里没有它。
//
// 没有它时,闭环仍然确定性地评 semantic / execution / result 三项。少掉的只是
// LLM 当裁判的那两个轴(faithfulness / relevancy),而那两项本来就要配模型才跑。
type JudgeReport struct {
	Available bool
}

// Judge reports "not available" and lets the caller carry on.
func Judge(_ context.Context, _ *Report) (*JudgeReport, error) {
	return &JudgeReport{Available: false}, nil
}

func (j *JudgeReport) WriteConsole(w io.Writer) {
	io.WriteString(w, "\n--- groundedness (LLM judge) ---\n  未编入:这一层在 -tags engineer 里(它会带进 agent 框架)\n")
}
