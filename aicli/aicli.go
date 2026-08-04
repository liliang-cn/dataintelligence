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

	// SSH runs the agent on another machine: an ssh destination, exactly as
	// ssh(1) takes it, so a ProxyJump or a bastion is ~/.ssh/config's problem
	// and not this code's. Empty runs it here.
	//
	// The agent must be installed *and authenticated* over there. Nothing here
	// can check the second half — a logged-out CLI fails at the first call with
	// its own message, which is the right place for it.
	SSH     string
	SSHOpts string
}

// DefaultTimeout is generous because these are one-shot engineer-time calls and
// a model asked to read a forty-table schema thinks for a while. Failing at
// thirty seconds would only teach people to retry.
const DefaultTimeout = 5 * time.Minute

// FromEnv builds a runner from DI_AGENT_CLI, or reports that none was asked for.
//
//	DI_AGENT_CLI=claude|codex|gemini
//	DI_AGENT_CLI_MODEL=<optional model override>
//	DI_AGENT_CLI_SSH=<user@host>   run the agent there instead of here
//	DI_AGENT_CLI_SSH_OPTS=<flags>  extra ssh flags, space separated
//	DI_AGENT_CLI_BIN=<path>        the agent's path, for a remote whose
//	                               non-interactive PATH does not have it
func FromEnv() (*Runner, bool) {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("DI_AGENT_CLI")))
	if name == "" {
		return nil, false
	}
	r, err := New(name, os.Getenv("DI_AGENT_CLI_MODEL"))
	if r != nil {
		r.SSH = strings.TrimSpace(os.Getenv("DI_AGENT_CLI_SSH"))
		r.SSHOpts = strings.TrimSpace(os.Getenv("DI_AGENT_CLI_SSH_OPTS"))
	}
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
	// A non-interactive ssh session gets a minimal PATH: on a machine where the
	// agent lives in ~/.local/bin — which is where its installer puts it — the
	// remote shell reports command-not-found and nothing says why. Naming the
	// binary is the fix, and it has to be possible.
	var opts []cliagent.Option
	if bin := strings.TrimSpace(os.Getenv("DI_AGENT_CLI_BIN")); bin != "" {
		opts = append(opts, cliagent.WithBinary(bin))
	}
	var p cliagent.Provider
	switch strings.ToLower(name) {
	case "claude":
		p = cliagent.NewClaude(opts...)
	case "codex":
		p = cliagent.NewCodex(opts...)
	case "gemini":
		p = cliagent.NewGemini(opts...)
	default:
		return nil, fmt.Errorf("unknown agent CLI (want claude, codex or gemini)")
	}
	r := &Runner{Provider: p, Name: strings.ToLower(name), Model: model, Timeout: DefaultTimeout}
	// Only look for the binary when it is meant to be here. With SSH set it
	// lives on the far side, and refusing to start because the laptop has no
	// claude installed would be exactly backwards.
	if os.Getenv("DI_AGENT_CLI_SSH") == "" {
		if _, err := exec.LookPath(r.Name); err != nil {
			return r, fmt.Errorf("%s is not on PATH (set DI_AGENT_CLI_SSH to run it on another machine)", name)
		}
	} else if _, err := exec.LookPath("ssh"); err != nil {
		return r, fmt.Errorf("DI_AGENT_CLI_SSH is set but ssh is not on PATH")
	}
	return r, nil
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
		// No MCP servers. Booting the operator's own took longer than the model
		// spent thinking, and a call that can reach them is not reproducible in
		// any sense. The built-in tools cannot be turned off from the command
		// line — `--allowed-tools ""` is accepted and changes nothing — so what
		// keeps them from mattering is the empty working directory and the
		// prompt: in print mode a write is denied rather than granted silently.
		NoMCP:   true,
		Sandbox: false,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", r.Name, err)
	}
	if len(spec.Argv) == 0 {
		return "", fmt.Errorf("%s: empty command", r.Name)
	}

	argv, env, workdir := spec.Argv, append(os.Environ(), spec.Env...), orDefault(spec.WorkDir, dir)
	if r.SSH != "" {
		script, serr := remoteScript(spec, dir)
		if serr != nil {
			return "", fmt.Errorf("%s: %w", r.Name, serr)
		}
		argv, serr = sshArgv(r.SSH, r.SSHOpts, script)
		if serr != nil {
			return "", fmt.Errorf("%s: %w", r.Name, serr)
		}
		// The remote decides its own environment and working directory; the
		// local ones would be wrong and the local temp dir does not exist over
		// there.
		env, workdir = os.Environ(), ""
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Env = env
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
	// 255 is ssh's own failure, not the agent's. Reported as an agent error it
	// sends the reader looking at the model instead of at the connection.
	if r.SSH != "" && exit == 255 {
		return "", fmt.Errorf("ssh %s: %s", r.SSH, snippet(errBuf.String()))
	}
	if r.SSH != "" && exit == 127 {
		return "", fmt.Errorf(
			"%s is not on %s's non-interactive PATH — an ssh session without a login shell "+
				"does not get ~/.local/bin, where the installer puts it. Set DI_AGENT_CLI_BIN "+
				"to its full path over there. (%s)",
			r.Name, r.SSH, snippet(errBuf.String()))
	}

	res, tail, err := sess.Finalize(ctx, out.Bytes(), exit)
	if err != nil {
		return "", fmt.Errorf("%s: %w (stderr: %s)", r.Name, err, snippet(errBuf.String()))
	}
	events = append(events, tail...)

	// Prefer the assistant's messages over the summary. The summary is the
	// CLI's own account of the session; the messages are what the model said,
	// and this call exists to get what the model said.
	// Filter on the role, not on whether a text field happens to be present.
	// Claude's system init, its hook events and the result summary all arrive
	// as agent.message — one trivial call produced eleven of them, ten being
	// hook lifecycle. They carry "raw" rather than "text", so checking for text
	// works today by coincidence and not by contract.
	var msgs []string
	for _, e := range events {
		if e.Type != cliagent.EventAgentMessage {
			continue
		}
		if role, _ := e.Payload["role"].(string); role != "" && role != "assistant" {
			continue
		}
		if t, _ := e.Payload["text"].(string); strings.TrimSpace(t) != "" {
			msgs = append(msgs, t)
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
	// The provider's own verdict first. A revoked token produces an assistant
	// message, is_error on the result frame, and exit zero — so neither the
	// text nor the exit code catches it, and the text of the "answer" would go
	// straight into a customer's model file.
	if res.Failed {
		return "", fmt.Errorf("%s%s failed: %s", r.Name, r.where(), hint(text))
	}
	if why := notAnAnswer(text); why != "" {
		return "", fmt.Errorf("%s%s: %s", r.Name, r.where(), why)
	}
	return text, nil
}

// where names the machine, so a failure that only happens remotely says so.
func (r *Runner) where() string {
	if r.SSH == "" {
		return ""
	}
	return " on " + r.SSH
}

// notAnAnswer is the fallback for providers with no failure signal.
//
// Codex and Gemini do not report a verdict — Result.Failed is only ever set by
// Claude — so for those two a short reply that is plainly an error message is
// all there is to go on. Matching on text is fragile and will miss new
// wordings; it is still the right trade, because a missed case leaves today's
// behaviour and a caught one turns a silent corruption into a message that
// names the fix.
func notAnAnswer(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	if len(t) > 200 {
		return "" // a real answer that happens to mention logging in
	}
	switch {
	case strings.Contains(t, "not logged in"), strings.Contains(t, "please run /login"):
		return authHint
	case strings.Contains(t, "failed to authenticate"), strings.Contains(t, "invalid api key"),
		strings.Contains(t, "authentication_error"), strings.Contains(t, "access token"):
		return "the agent rejected its credentials: " + strings.TrimSpace(text)
	case strings.Contains(t, "usage limit") && strings.Contains(t, "reached"):
		return "the agent is rate limited: " + strings.TrimSpace(text)
	}
	return ""
}

// hint adds the one piece of context the message cannot carry: why a working
// local agent stops working over ssh.
func hint(text string) string {
	t := strings.ToLower(text)
	if strings.Contains(t, "not logged in") || strings.Contains(t, "authenticate") ||
		strings.Contains(t, "access token") {
		return strings.TrimSpace(text) + "\n  " + authHint
	}
	return snippet(text)
}

// authHint is the failure that only happens remotely, and only on macOS.
const authHint = "the agent is not authenticated there. On macOS its credentials live in the " +
	"Keychain, which an ssh session cannot unlock; on Linux they are a file in ~/.claude. " +
	"Log in on that host, or run the agent locally."

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
