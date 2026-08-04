// Package aicli runs a coding-agent CLI — Claude Code, Codex, Gemini — as the
// LLM behind an engineer's own commands.
//
// The point is not to save an API key. It is that the tasks in this repo that
// want a model are engineer-time tasks, run once, on a laptop, and they already
// have an agent installed and authenticated: drafting a semantic model from a
// schema, triaging a cross-source conflict, judging whether an answer was
// grounded. A subprocess that takes three seconds is free at that cadence.
//
// It is deliberately *not* wired into `di serve`, and the reason is worth
// stating rather than leaving as an omission:
//
//   - Latency. A governed query answers in about ten milliseconds. Spending
//     three seconds spawning a CLI to decide which metric was meant makes the
//     slow part of the request the part that did not touch the database.
//   - Concurrency. Every call is a whole process. Ten people asking at once is
//     ten of them.
//   - Licensing. A Claude Code or Codex subscription is for a developer working
//     interactively. Making it the inference backend of software sold to a
//     customer is not that, and it would also mean the customer's product stops
//     working when the engineer's personal subscription lapses.
//
// So: the engineer's commands, yes. The product's request path, no — that stays
// on LLM_BASE_URL, where a customer can point it at their own endpoint or at a
// model running inside their own network.
package aicli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agentcli/cliagent"
)

// Ask is the one-shot completion shape the rest of this repo already uses:
// modelgen.AskFunc and reconcile.AskFunc are both this.
type Ask func(ctx context.Context, prompt string) (string, error)

// Runner invokes one CLI agent.
type Runner struct {
	Provider cliagent.Provider
	Name     string
	Model    string
	Timeout  time.Duration
}

// DefaultTimeout is generous because these are one-shot engineer-time calls and
// a model asked to read a forty-table schema thinks for a while. Failing at
// thirty seconds would only teach people to retry.
const DefaultTimeout = 5 * time.Minute

// FromEnv builds a runner from DI_AGENT_CLI, or reports that none was asked for.
//
//	DI_AGENT_CLI=claude|codex|gemini
//	DI_AGENT_CLI_MODEL=<optional model override>
func FromEnv() (*Runner, bool) {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("DI_AGENT_CLI")))
	if name == "" {
		return nil, false
	}
	r, err := New(name, os.Getenv("DI_AGENT_CLI_MODEL"))
	if err != nil {
		// A misspelled name is worth a word: silently falling back to the
		// heuristic path produces a draft that looks like the LLM ran.
		fmt.Fprintf(os.Stderr, "-- DI_AGENT_CLI=%q: %v\n", name, err)
		return nil, false
	}
	return r, true
}

// New builds a runner for a named CLI agent.
func New(name, model string) (*Runner, error) {
	var p cliagent.Provider
	switch strings.ToLower(name) {
	case "claude":
		p = cliagent.NewClaude()
	case "codex":
		p = cliagent.NewCodex()
	case "gemini":
		p = cliagent.NewGemini()
	default:
		return nil, fmt.Errorf("unknown agent CLI (want claude, codex or gemini)")
	}
	if _, err := exec.LookPath(strings.ToLower(name)); err != nil {
		return nil, fmt.Errorf("%s is not on PATH", name)
	}
	return &Runner{Provider: p, Name: strings.ToLower(name), Model: model, Timeout: DefaultTimeout}, nil
}

// Ask returns the runner as the Ask func the rest of the repo takes.
func (r *Runner) Ask() Ask { return r.ask }

func (r *Runner) ask(ctx context.Context, prompt string) (string, error) {
	// An empty temp directory, not the current one.
	//
	// A coding-agent CLI reads the project it is standing in: CLAUDE.md,
	// AGENTS.md, skills, the git status. All of that is injected into the
	// prompt. Run from this repo and the model drafting a customer's semantic
	// model is also being told, at length, how this repo likes its Go
	// comments. Used as an inference backend that is contamination, and it is
	// invisible — the answer is merely a little different than it would have
	// been, for reasons nothing in the output mentions.
	dir, err := os.MkdirTemp("", "di-aicli-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sess := r.Provider.NewSession()
	spec, err := sess.BuildCommand(ctx, cliagent.Request{
		Prompt:        prompt,
		WorkspacePath: dir,
		Model:         r.Model,
		ExtraArgs:     r.quietArgs(dir),
		Sandbox:       false,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", r.Name, err)
	}
	if len(spec.Argv) == 0 {
		return "", fmt.Errorf("%s: empty command", r.Name)
	}

	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = orDefault(spec.WorkDir, dir)
	cmd.Env = append(os.Environ(), spec.Env...)
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	runErr := cmd.Run()

	// Events come from ParseChunk, not from Finalize. Finalize takes the output
	// argument and ignores it — it only flushes what the session has already
	// buffered — so handing it everything and asking for the answer returns an
	// empty result and no error at all.
	events, perr := sess.ParseChunk(out.Bytes())
	if perr != nil {
		return "", fmt.Errorf("%s: %w", r.Name, perr)
	}

	exit := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			return "", fmt.Errorf("%s: %w", r.Name, runErr)
		}
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s: gave up after %s", r.Name, timeout)
	}

	res, tail, err := sess.Finalize(ctx, out.Bytes(), exit)
	if err != nil {
		return "", fmt.Errorf("%s: %w (stderr: %s)", r.Name, err, snippet(errBuf.String()))
	}
	events = append(events, tail...)

	// Prefer the assistant's messages over the summary. The summary is the
	// CLI's own account of the session; the messages are what the model said,
	// and this call exists to get what the model said.
	var msgs []string
	for _, e := range events {
		if e.Type == cliagent.EventAgentMessage {
			if t, _ := e.Payload["text"].(string); strings.TrimSpace(t) != "" {
				msgs = append(msgs, t)
			}
		}
	}
	text := strings.TrimSpace(strings.Join(msgs, "\n"))
	if text == "" {
		text = strings.TrimSpace(res.Summary)
	}
	if text == "" {
		return "", fmt.Errorf("%s: no answer (exit %d, stderr: %s)", r.Name, exit, snippet(errBuf.String()))
	}
	if exit != 0 && len(msgs) == 0 {
		return "", fmt.Errorf("%s: exit %d: %s", r.Name, exit, snippet(text))
	}
	return text, nil
}

// quietArgs strips the agent down towards a completion.
//
// MCP is the part that matters and the part that works: booting every
// configured server took longer than the model spent thinking, and a call that
// reaches the engineer's own MCP servers is not reproducible in any sense. An
// empty config plus the strict flag brings it to zero, measurably.
//
// The built-in tools cannot be turned off from the command line — `--allowed
// -tools ""` is accepted and changes nothing, so it is not passed: a flag that
// looks like it disables something and does not is worse than no flag. What
// stops them mattering is the empty working directory and the prompt. In print
// mode a write is denied rather than granted silently, so the worst case is an
// answer saying it could not write the file, which is visible.
//
// The config path goes before the strict flag on purpose: --mcp-config is
// variadic, and given the JSON inline it swallowed the prompt as a second
// config path and failed with a file-not-found naming the whole prompt.
func (r *Runner) quietArgs(dir string) []string {
	switch r.Name {
	case "claude":
		path := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
			return nil
		}
		return []string{"--mcp-config", path, "--strict-mcp-config"}
	default:
		// Codex and Gemini already get their headless flags from the provider.
		// Adding --skip-git-repo-check here passed it twice, and clap rejects a
		// repeated flag outright.
		return nil
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
