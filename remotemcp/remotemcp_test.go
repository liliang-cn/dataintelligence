package remotemcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testServer is a tiny MCP server over streamable HTTP (stateless, Bearer),
// shaped like the external servers this package talks to.
func testServer(t *testing.T, token string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var commands atomic.Int32
	srv := mcp.NewServer(&mcp.Implementation{Name: "test-plant", Version: "0"}, nil)
	type siteArgs struct {
		Prefix string `json:"prefix,omitempty" jsonschema:"filter by name prefix"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_sites", Description: "List sites."},
		func(_ context.Context, _ *mcp.CallToolRequest, a siteArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `["north","south"]` + a.Prefix}}}, nil, nil
		})
	type cmdArgs struct {
		Target string `json:"target"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "send_command", Description: "Change something."},
		func(_ context.Context, _ *mcp.CallToolRequest, a cmdArgs) (*mcp.CallToolResult, any, error) {
			commands.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done " + a.Target}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "wipe", Description: "Not allow-listed anywhere."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "wiped"}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "broken", Description: "Reports an error."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "line offline"}}}, nil, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, &commands
}

func newSet(t *testing.T, url string) *Set {
	t.Helper()
	t.Setenv("TEST_PLANT_TOKEN", "s3cret")
	s, err := New([]Server{{Name: "plant", URL: url, TokenEnv: "TEST_PLANT_TOKEN",
		Tools: []string{"list_sites", "broken"}, Actions: []string{"send_command"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestReadToolsCarryRemoteSchemasAndPrefixes(t *testing.T) {
	ts, _ := testServer(t, "s3cret")
	s := newSet(t, ts.URL)
	specs := s.ReadTools(context.Background())
	if len(specs) != 2 || specs[0].Name != "plant__list_sites" {
		t.Fatalf("specs = %+v", specs)
	}
	props, _ := specs[0].Schema["properties"].(map[string]any)
	if _, ok := props["prefix"]; !ok || !strings.Contains(specs[0].Description, "List sites") {
		t.Fatalf("remote schema/description not carried: %+v", specs[0])
	}
	for _, sp := range specs {
		if sp.Tool == "send_command" || sp.Tool == "wipe" {
			t.Fatalf("%s must not be exposed to the agent", sp.Tool)
		}
	}
}

func TestCallSendsTheBearerToken(t *testing.T) {
	ts, _ := testServer(t, "s3cret")
	s := newSet(t, ts.URL)
	out, err := s.Call(context.Background(), "plant", "list_sites", map[string]any{"prefix": "!"})
	if err != nil || out != `["north","south"]!` {
		t.Fatalf("out = %q err = %v", out, err)
	}
}

func TestAWrongTokenFails(t *testing.T) {
	ts, _ := testServer(t, "other")
	s := newSet(t, ts.URL)
	if _, err := s.Call(context.Background(), "plant", "list_sites", nil); err == nil {
		t.Fatal("expected an auth failure")
	}
}

func TestActionsAreNotCallableAsTools(t *testing.T) {
	ts, commands := testServer(t, "s3cret")
	s := newSet(t, ts.URL)
	ctx := context.Background()
	if _, err := s.Call(ctx, "plant", "send_command", map[string]any{"target": "x"}); err == nil || !strings.Contains(err.Error(), "adopted") {
		t.Fatalf("an action must be refused as a direct call: %v", err)
	}
	if _, err := s.Call(ctx, "plant", "wipe", nil); err == nil {
		t.Fatal("a tool in neither list must be refused")
	}
	if err := s.ValidateAction("plant", "list_sites"); err == nil {
		t.Fatal("a read-only tool is not an action")
	}
	if commands.Load() != 0 {
		t.Fatal("nothing should have reached send_command")
	}
	out, err := s.CallAction(ctx, "plant", "send_command", map[string]any{"target": "line-1"})
	if err != nil || out != "done line-1" || commands.Load() != 1 {
		t.Fatalf("out = %q err = %v n = %d", out, err, commands.Load())
	}
}

func TestAToolErrorIsAnError(t *testing.T) {
	ts, _ := testServer(t, "s3cret")
	s := newSet(t, ts.URL)
	out, err := s.Call(context.Background(), "plant", "broken", nil)
	if err == nil || out != "line offline" {
		t.Fatalf("out = %q err = %v", out, err)
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []Server{
		{Name: "a__b", URL: "http://x"},
		{Name: "a", URL: ""},
		{Name: "a", URL: "http://x", Tools: []string{"t"}, Actions: []string{"t"}},
		{Name: "a", URL: "http://x", TokenEnv: "DEFINITELY_UNSET_TOKEN_ENV"},
	}
	for _, c := range cases {
		if _, err := New([]Server{c}); err == nil {
			t.Errorf("expected %+v to be refused", c)
		}
	}
	if _, err := New([]Server{{Name: "a", URL: "http://x"}, {Name: "a", URL: "http://y"}}); err == nil {
		t.Error("duplicate names must be refused")
	}
}
