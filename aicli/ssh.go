package aicli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/liliang-cn/agentcli/cliagent"
)

// remoteScript wraps a locally-built CommandSpec into one shell command to run
// over ssh.
//
// ssh is the whole transport: nothing is installed on the far side, the
// authentication is already solved by keys, and a ProxyJump or a bastion is the
// user's ~/.ssh/config problem rather than this code's. What it costs is that
// argv becomes a string, and two things in that string are not obvious.
//
// The workspace does not travel by itself. BuildCommand writes the MCP config
// on *this* machine, so the argv it produces names a path the remote host does
// not have — the flag points at nothing, the CLI errors, and the message is
// about a missing file rather than about a missing transport. So the files are
// carried inline and the paths are rewritten to the remote temp directory.
//
// Everything is single-quoted. The prompt is a schema dump: it has newlines,
// quotes, JSON, and Chinese in it, and joining argv with spaces would at best
// break and at worst run part of a customer's data as a shell command.
func remoteScript(spec cliagent.CommandSpec, localDir string) (string, error) {
	const marker = "DI_AICLI_EOF_9f3a"

	var b strings.Builder
	b.WriteString(`set -e; D=$(mktemp -d); trap 'rm -rf "$D"' EXIT; `)

	files, err := os.ReadDir(localDir)
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(localDir, f.Name()))
		if err != nil {
			return "", err
		}
		// A quoted heredoc delimiter means the shell expands nothing inside it,
		// which is what makes arbitrary JSON safe to carry. The one thing that
		// would break it is content containing the delimiter on its own line.
		if lineEquals(string(body), marker) {
			return "", fmt.Errorf("workspace file %s contains the heredoc delimiter", f.Name())
		}
		fmt.Fprintf(&b, "cat > \"$D\"/%s <<'%s'\n%s\n%s\n", shellQuote(f.Name()), marker,
			strings.TrimRight(string(body), "\n"), marker)
	}

	b.WriteString(`cd "$D"; `)
	if len(spec.Env) > 0 {
		b.WriteString("env")
		for _, kv := range spec.Env {
			b.WriteString(" " + rewriteDir(shellQuote(kv), localDir))
		}
		b.WriteString(" ")
	}
	for i, a := range spec.Argv {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(rewriteDir(shellQuote(a), localDir))
	}
	return b.String(), nil
}

// maxRemoteCommand bounds the command line.
//
// ARG_MAX is a megabyte here and two on Linux, and the whole script travels as
// one argument to ssh. A schema dump for a wide warehouse is the realistic way
// to approach it, and "argument list too long" is not a message anyone traces
// back to a prompt being large.
const maxRemoteCommand = 400 << 10

// sshArgv builds the ssh invocation.
//
// BatchMode is not optional: without it a host whose key is not set up stops
// and asks for a password from a process nobody is watching, and the command
// hangs until its timeout instead of failing in a second with a reason.
func sshArgv(dest, extraOpts, script string) ([]string, error) {
	if len(script) > maxRemoteCommand {
		return nil, fmt.Errorf(
			"the prompt is %d KB and the remote command line tops out around %d KB — "+
				"narrow the model generation to fewer tables, or run the agent locally",
			len(script)>>10, maxRemoteCommand>>10)
	}
	// -n detaches the remote command's stdin from ours.
	//
	// ssh forwards the parent's stdin by default. codex announces "Reading
	// additional input from stdin..." and waits on it, so a run from a script
	// or a CI job either consumes whatever the parent had queued or blocks
	// until the timeout — for a reason that appears nowhere in the output.
	argv := []string{"ssh", "-n", "-o", "BatchMode=yes"}
	if extraOpts != "" {
		argv = append(argv, strings.Fields(extraOpts)...)
	}
	return append(argv, dest, script), nil
}

// shellQuote wraps s so the remote shell reads it as one literal word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// rewriteDir points a quoted argument at the remote temp directory.
//
// The local path sits inside single quotes, so closing them, expanding $D and
// reopening is the substitution: '/tmp/x/mcp.json' becomes ”"$D"'/mcp.json',
// which the shell reads as the empty string, then $D, then the rest.
func rewriteDir(quoted, localDir string) string {
	if localDir == "" {
		return quoted
	}
	return strings.ReplaceAll(quoted, localDir, `'"$D"'`)
}

func lineEquals(body, marker string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimRight(line, "\r") == marker {
			return true
		}
	}
	return false
}
