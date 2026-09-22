//go:build !engineer

// Package aicli — 默认构建里的空壳。
//
// 真正的实现(用你已装好的 Claude Code / Codex / Gemini CLI 当模型)会把
// agentcli 拽进来,而 agentcli 又会拽进一整个 agent 框架。**一个数据平台不该
// 在它的依赖图里带着 agent 框架**:升级 agent 那侧可能弄坏数据这侧,而客户
// 看模块清单时得听人解释为什么。
//
// 那个功能本身是好的,只是它属于工程师的笔记本,不属于交付给客户的二进制。
// 所以它在一个 build tag 后面:
//
//	go build -tags engineer ./cmd/di
//
// 没有它时,要模型的那几条命令回退到 LLM_BASE_URL/LLM_API_KEY/LLM_MODEL,
// 也就是产品本来就在用的那条路。
package aicli

import "context"

// Ask is one prompt in, one completion out.
type Ask func(ctx context.Context, prompt string) (string, error)

// Runner is the CLI-agent runner. Absent in the default build.
type Runner struct {
	Name  string
	Model string
}

// Ask returns nil: this build has no CLI agent behind it.
func (r *Runner) Ask() Ask { return nil }

// FromEnv always reports "not configured" here, so callers fall through to the
// ordinary LLM path instead of failing.
func FromEnv() (*Runner, bool) { return nil, false }
