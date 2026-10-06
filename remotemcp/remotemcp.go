// Package remotemcp connects DI to external MCP servers declared in config, and
// splits each server's tools into two kinds:
//
//   - tools: read-only lookups an agent may call directly while it investigates
//     (exposed to the copilot as "<server>__<tool>");
//   - actions: tools that change something in the world. An agent can never
//     call them. They can only be named inside a proposed plan, and they run
//     when a person other than the proposer adopts it.
//
// Which tool is which is the deployment's decision, made in config; nothing
// here knows what any tool does. A tool not listed in either is unreachable.
//
// The client is agent-go's MCP client over streamable HTTP, with a Bearer
// token read from the environment variable the config names.
package remotemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	agentmcp "github.com/liliang-cn/agent-go/v3/pkg/mcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server is one external MCP server.
type Server struct {
	Name     string   `yaml:"name" json:"name"`
	URL      string   `yaml:"url" json:"url"`
	TokenEnv string   `yaml:"token_env" json:"token_env"`
	Tools    []string `yaml:"tools" json:"tools"`
	Actions  []string `yaml:"actions" json:"actions"`
}

// Sep joins a server name and a tool name in the agent-facing tool name.
const Sep = "__"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Set is every configured server.
type Set struct {
	order []string
	conns map[string]*conn
}

type conn struct {
	cfg   Server
	token string

	mu sync.Mutex
	c  *agentmcp.Client
}

// New validates the configuration. It does not connect: a server that is down
// at boot fails its own calls, not the whole service.
func New(servers []Server) (*Set, error) {
	s := &Set{conns: map[string]*conn{}}
	for i, sv := range servers {
		where := fmt.Sprintf("copilot.mcp[%d]", i)
		if !nameRe.MatchString(sv.Name) || strings.Contains(sv.Name, Sep) {
			return nil, fmt.Errorf("%s: name %q must be letters, digits, '-' or '_' (and no %q)", where, sv.Name, Sep)
		}
		if _, dup := s.conns[sv.Name]; dup {
			return nil, fmt.Errorf("%s: duplicate server name %q", where, sv.Name)
		}
		if strings.TrimSpace(sv.URL) == "" {
			return nil, fmt.Errorf("%s (%s): url is required", where, sv.Name)
		}
		for _, t := range sv.Tools {
			if slices.Contains(sv.Actions, t) {
				return nil, fmt.Errorf("%s (%s): %q is listed under both tools and actions — a tool is either something the agent may call or something only an adopted plan may run, not both", where, sv.Name, t)
			}
		}
		c := &conn{cfg: sv}
		if sv.TokenEnv != "" {
			c.token = os.Getenv(sv.TokenEnv)
			if c.token == "" {
				return nil, fmt.Errorf("%s (%s): token_env %s is empty", where, sv.Name, sv.TokenEnv)
			}
		}
		s.conns[sv.Name] = c
		s.order = append(s.order, sv.Name)
	}
	return s, nil
}

// Names lists the configured servers in config order.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.order...)
}

// Close disconnects every server.
func (s *Set) Close() {
	if s == nil {
		return
	}
	for _, c := range s.conns {
		c.mu.Lock()
		if c.c != nil {
			_ = c.c.Close()
			c.c = nil
		}
		c.mu.Unlock()
	}
}

func (c *conn) client(ctx context.Context, fresh bool) (*agentmcp.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c != nil && c.c.IsConnected() && !fresh {
		return c.c, nil
	}
	if c.c != nil {
		_ = c.c.Close()
		c.c = nil
	}
	cfg := &agentmcp.ServerConfig{Name: c.cfg.Name, Type: agentmcp.ServerTypeHTTP, URL: c.cfg.URL, DefaultTimeout: 20 * time.Second}
	if c.token != "" {
		cfg.Headers = map[string]string{"Authorization": "Bearer " + c.token}
	}
	cl, err := agentmcp.NewClient(cfg, &agentmcp.ClientOptions{})
	if err != nil {
		return nil, err
	}
	if err := cl.Connect(ctx); err != nil {
		return nil, fmt.Errorf("mcp server %s: %w", c.cfg.Name, err)
	}
	c.c = cl
	return cl, nil
}

func (s *Set) conn(server string) (*conn, error) {
	if s == nil {
		return nil, fmt.Errorf("no external MCP servers are configured")
	}
	c, ok := s.conns[server]
	if !ok {
		return nil, fmt.Errorf("no MCP server named %q is configured (have: %s)", server, strings.Join(s.order, ", "))
	}
	return c, nil
}

// ToolSpec is one read-only tool as the agent sees it.
type ToolSpec struct {
	Name        string // "<server>__<tool>"
	Server      string
	Tool        string
	Description string
	Schema      map[string]any // the remote input schema; a permissive object when unknown
}

// ReadTools lists the allow-listed read-only tools, with the remote server's
// descriptions and schemas when it is reachable. An unreachable server still
// contributes its tools (with a permissive schema), so the agent can try and
// get the real error rather than not knowing the tool exists.
func (s *Set) ReadTools(ctx context.Context) []ToolSpec {
	if s == nil {
		return nil
	}
	var out []ToolSpec
	for _, name := range s.order {
		c := s.conns[name]
		var remote map[string]*mcp.Tool
		if cl, err := c.client(ctx, false); err == nil {
			remote = cl.GetTools()
		}
		for _, t := range c.cfg.Tools {
			spec := ToolSpec{
				Name: name + Sep + t, Server: name, Tool: t,
				Description: fmt.Sprintf("[%s] %s (external, read-only)", name, t),
				Schema:      map[string]any{"type": "object", "additionalProperties": true},
			}
			if rt, ok := remote[t]; ok {
				if rt.Description != "" {
					spec.Description = fmt.Sprintf("[%s] %s", name, rt.Description)
				}
				if sc := schemaMap(rt.InputSchema); sc != nil {
					spec.Schema = sc
				}
			}
			out = append(out, spec)
		}
	}
	return out
}

// ActionSpecs describes the action tools, for telling an agent what it may
// name in a plan.
func (s *Set) ActionSpecs(ctx context.Context) []ToolSpec {
	if s == nil {
		return nil
	}
	var out []ToolSpec
	for _, name := range s.order {
		c := s.conns[name]
		var remote map[string]*mcp.Tool
		if cl, err := c.client(ctx, false); err == nil {
			remote = cl.GetTools()
		}
		for _, t := range c.cfg.Actions {
			spec := ToolSpec{Name: name + Sep + t, Server: name, Tool: t}
			if rt, ok := remote[t]; ok {
				spec.Description = rt.Description
				spec.Schema = schemaMap(rt.InputSchema)
			}
			out = append(out, spec)
		}
	}
	return out
}

func schemaMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m == nil {
		return nil
	}
	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}
	return m
}

// Call runs an allow-listed read-only tool.
func (s *Set) Call(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	c, err := s.conn(server)
	if err != nil {
		return "", err
	}
	if !slices.Contains(c.cfg.Tools, tool) {
		if slices.Contains(c.cfg.Actions, tool) {
			return "", fmt.Errorf("%s%s%s is an action: it runs only as part of a plan a person has adopted, never directly", server, Sep, tool)
		}
		return "", fmt.Errorf("%s%s%s is not an allowed tool", server, Sep, tool)
	}
	return c.call(ctx, tool, args)
}

// ValidateAction reports whether server/tool is configured as an action.
func (s *Set) ValidateAction(server, tool string) error {
	c, err := s.conn(server)
	if err != nil {
		return err
	}
	if !slices.Contains(c.cfg.Actions, tool) {
		return fmt.Errorf("%s is not among %s's configured actions (%s)", tool, server, strings.Join(c.cfg.Actions, ", "))
	}
	return nil
}

// CallAction runs an action tool. Only the consulting loop calls this, after
// an adoption.
func (s *Set) CallAction(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	if err := s.ValidateAction(server, tool); err != nil {
		return "", err
	}
	c, _ := s.conn(server)
	return c.call(ctx, tool, args)
}

// call invokes a tool, reconnecting once if the session went away (a server
// restart, a missed heartbeat). A tool that reports isError is an error, with
// its message kept as the result.
func (c *conn) call(ctx context.Context, tool string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	var res *agentmcp.ToolResult
	for attempt := 0; attempt < 2; attempt++ {
		cl, err := c.client(ctx, attempt > 0)
		if err != nil {
			return "", err
		}
		res, err = cl.CallTool(ctx, tool, args)
		if err == nil && res != nil && res.Success {
			break
		}
		if attempt == 1 {
			if err != nil {
				return "", err
			}
			if res != nil {
				return "", fmt.Errorf("%s%s%s: %s", c.cfg.Name, Sep, tool, res.Error)
			}
		}
	}
	text := render(res.Data)
	if res.IsError {
		return text, fmt.Errorf("%s%s%s reported an error: %s", c.cfg.Name, Sep, tool, text)
	}
	return text, nil
}

func render(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}
