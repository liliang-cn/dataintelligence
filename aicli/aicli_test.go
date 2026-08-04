package aicli

import (
	"strings"
	"testing"
)

func TestUnknownAgentIsNamed(t *testing.T) {
	if _, err := New("gpt", ""); err == nil || !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should list the options, got %v", err)
	}
}

// An unset variable means "not asked for", not "misconfigured".
func TestFromEnvSilentWhenUnset(t *testing.T) {
	t.Setenv("DI_AGENT_CLI", "")
	if _, ok := FromEnv(); ok {
		t.Error("FromEnv returned a runner with nothing configured")
	}
}

// A name that is set but wrong must not fall through quietly: the command then
// produces a heuristic draft that looks like the model ran.
func TestFromEnvRejectsAnUnknownName(t *testing.T) {
	t.Setenv("DI_AGENT_CLI", "gpt")
	if _, ok := FromEnv(); ok {
		t.Error("FromEnv accepted an unknown agent")
	}
}

// Over ssh on macOS the CLI returns "Not logged in" as an assistant message and
// exits zero. Taken at face value that becomes the LLM's contribution to a
// customer's semantic model.
func TestAuthFailureIsNotAcceptedAsAnAnswer(t *testing.T) {
	for _, s := range []string{
		"Not logged in · Please run /login",
		"  not logged in  ",
		"Invalid API key",
	} {
		if notAnAnswer(s) == "" {
			t.Errorf("%q was accepted as a model answer", s)
		}
	}
}

// A real answer that happens to mention logging in must survive.
func TestLongAnswersAreNotSecondGuessed(t *testing.T) {
	long := "metrics:\n  - name: login_count\n    description: how many users are not logged in yet" +
		strings.Repeat(" and more model YAML", 20)
	if why := notAnAnswer(long); why != "" {
		t.Errorf("a real answer was rejected: %s", why)
	}
}
