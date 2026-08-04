package aicli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agentcli/cliagent"
)

func workspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// BuildCommand writes the MCP config on this machine, so the argv it produces
// names a path the remote host does not have. The file has to travel and the
// path has to be rewritten, or the CLI fails complaining about a missing file
// and nothing points at the transport.
func TestWorkspaceTravelsAndPathsAreRewritten(t *testing.T) {
	dir := workspace(t, map[string]string{"mcp.json": `{"mcpServers":{}}`})
	script, err := remoteScript(cliagent.CommandSpec{
		Argv: []string{"claude", "--mcp-config", filepath.Join(dir, "mcp.json"), "--strict-mcp-config", "hello"},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `{"mcpServers":{}}`) {
		t.Error("the config did not travel")
	}
	if strings.Contains(script, dir) {
		t.Errorf("a local path survived into the remote script:\n%s", script)
	}
	if !strings.Contains(script, `'"$D"'/mcp.json`) {
		t.Errorf("the path was not rewritten to the remote temp dir:\n%s", script)
	}
}

// The prompt is a schema dump: newlines, quotes, JSON, Chinese. Joining argv
// with spaces would at best break and at worst run part of a customer's data as
// a shell command.
func TestHostilePromptSurvivesQuoting(t *testing.T) {
	dir := workspace(t, nil)
	nasty := "line1\n'; rm -rf /; echo '\n{\"a\":\"b\"}\n各车间的电耗 $HOME `id`"
	script, err := remoteScript(cliagent.CommandSpec{Argv: []string{"claude", "-p", nasty}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, danger := range []string{"; rm -rf /;", "`id`", "$HOME"} {
		// Present as literal text is fine; present *outside* single quotes is not.
		if outsideQuotes(script, danger) {
			t.Errorf("%q is unquoted in the remote script:\n%s", danger, script)
		}
	}
}

// outsideQuotes reports whether needle appears while the shell is not inside a
// single-quoted string.
//
// Outside quotes a backslash escapes the next character, which is what makes
// the '\” idiom work: close, an escaped quote, reopen. A checker that misses
// that reads correct escaping as an injection.
func outsideQuotes(script, needle string) bool {
	inQuote := false
	for i := 0; i < len(script); i++ {
		switch {
		case script[i] == '\\' && !inQuote:
			i++ // the escaped character is literal, never a delimiter
			continue
		case script[i] == '\'':
			inQuote = !inQuote
			continue
		}
		if !inQuote && strings.HasPrefix(script[i:], needle) {
			return true
		}
	}
	return false
}

// "argument list too long" is not a message anyone traces back to a prompt
// being large.
func TestOversizedPromptIsRefusedWithAReason(t *testing.T) {
	_, err := sshArgv("host", "", strings.Repeat("x", maxRemoteCommand+1))
	if err == nil || !strings.Contains(err.Error(), "KB") {
		t.Errorf("want a size error naming the limit, got %v", err)
	}
}

// Without BatchMode a host whose key is not set up stops and asks for a
// password from a process nobody is watching.
func TestSSHFailsFastRatherThanPrompting(t *testing.T) {
	argv, err := sshArgv("u@h", "-p 2222", "true")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "BatchMode=yes") {
		t.Errorf("argv = %v", argv)
	}
	// ssh forwards the parent's stdin; codex waits on it and blocks.
	if !strings.Contains(joined, " -n ") {
		t.Errorf("argv must detach stdin: %v", argv)
	}
	if !strings.Contains(joined, "-p 2222") {
		t.Errorf("extra opts dropped: %v", argv)
	}
	if argv[len(argv)-1] != "true" || argv[len(argv)-2] != "u@h" {
		t.Errorf("destination and script must be last: %v", argv)
	}
}

// The static checks above are a reading of the quoting rules. This one asks the
// shell, which is the only authority on them.
func TestRealShellReceivesThePromptVerbatim(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no shell")
	}
	dir := workspace(t, map[string]string{"mcp.json": `{"mcpServers":{}}`})
	nasty := "line1\n'; rm -rf /tmp/di-should-not-exist; echo '\n{\"a\":\"b\"}\n各车间的电耗 $HOME `id`"

	// printf %s of the last argument: whatever the shell hands the program is
	// what a real CLI would receive.
	script, err := remoteScript(cliagent.CommandSpec{
		Argv: []string{"printf", "%s", nasty},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if string(out) != nasty {
		t.Errorf("the shell delivered\n%q\nwant\n%q", out, nasty)
	}
}

// The workspace has to arrive with its bytes intact, and the temp directory has
// to be gone afterwards.
func TestRealShellWritesTheWorkspaceAndCleansUp(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no shell")
	}
	body := `{"mcpServers":{},"note":"quotes ' and $D and \u4e2d\u6587"}`
	dir := workspace(t, map[string]string{"mcp.json": body})
	script, err := remoteScript(cliagent.CommandSpec{
		Argv: []string{"sh", "-c", `cat "$0"/mcp.json; printf " DIR=%s" "$0"`, filepath.Join(dir, "")},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := string(out)
	if !strings.HasPrefix(got, body) {
		t.Errorf("config arrived as %q, want %q", got, body)
	}
	remote := strings.TrimPrefix(got[len(body):], " DIR=")
	remote = strings.TrimSuffix(remote, "/")
	if remote == dir || remote == "" {
		t.Fatalf("the command ran in %q, not a remote temp dir", remote)
	}
	if _, err := os.Stat(remote); err == nil {
		t.Errorf("%s survived the run — the trap did not fire", remote)
	}
}
