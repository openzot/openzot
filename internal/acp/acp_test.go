package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/chatbotkit/go-sdk/agent"
	"github.com/chatbotkit/go-sdk/sdk"
)

func TestPromptText(t *testing.T) {
	uri := "file:///tmp/notes.md"

	tests := []struct {
		name   string
		blocks []acpsdk.ContentBlock
		want   string
	}{
		{
			name: "text blocks are joined",
			blocks: []acpsdk.ContentBlock{
				{Text: &acpsdk.ContentBlockText{Text: "fix the parser"}},
				{Text: &acpsdk.ContentBlockText{Text: "  and add a test  "}},
			},
			want: "fix the parser\n\nand add a test",
		},
		{
			name: "resource links are named so the agent can read them",
			blocks: []acpsdk.ContentBlock{
				{Text: &acpsdk.ContentBlockText{Text: "review this"}},
				{ResourceLink: &acpsdk.ContentBlockResourceLink{Name: "notes", Uri: uri}},
			},
			want: "review this\n\n[resource] notes: " + uri,
		},
		{
			name:   "empty content yields an empty prompt",
			blocks: []acpsdk.ContentBlock{{Text: &acpsdk.ContentBlockText{Text: "   "}}},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promptText(tt.blocks); got != tt.want {
				t.Errorf("promptText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnterDirRestores(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	dir := t.TempDir()
	restore, err := enterDir(dir)
	if err != nil {
		t.Fatalf("enterDir: %v", err)
	}

	during, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// macOS hands out symlinked temp dirs, so compare resolved paths.
	if want, _ := filepath.EvalSymlinks(dir); want != during {
		t.Errorf("working directory = %q, want %q", during, want)
	}

	restore()

	after, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if after != before {
		t.Errorf("working directory after restore = %q, want %q", after, before)
	}
}

func TestEnterDirMissing(t *testing.T) {
	if _, err := enterDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("entering a missing directory should fail")
	}
}

func TestMcpNames(t *testing.T) {
	servers := []acpsdk.McpServer{
		{Stdio: &acpsdk.McpServerStdio{Name: "buzz"}},
		{Http: &acpsdk.McpServerHttpInline{Name: "docs"}},
		{}, // an unknown transport contributes nothing
	}

	got := mcpNames(servers)
	if len(got) != 2 || got[0] != "buzz" || got[1] != "docs" {
		t.Errorf("mcpNames() = %v, want [buzz docs]", got)
	}
}

// peer drives a server over a pipe pair, speaking raw JSON-RPC so the tests
// check the bytes a real client would see.
type peer struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Reader
	id  int
}

func newPeer(t *testing.T, opts Options) *peer {
	t.Helper()

	serverIn, clientWrites := io.Pipe()
	clientReads, serverOut := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = serve(ctx, opts, serverIn, serverOut)
	}()

	t.Cleanup(func() {
		cancel()
		_ = clientWrites.Close()
		_ = clientReads.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not shut down")
		}
	})

	return &peer{t: t, in: clientWrites, out: bufio.NewReader(clientReads)}
}

// call sends a request and returns its result, or the error object if the
// server rejected it.
func (p *peer) call(method string, params any) (json.RawMessage, *json.RawMessage) {
	p.t.Helper()

	p.id++
	req := map[string]any{"jsonrpc": "2.0", "id": p.id, "method": method, "params": params}
	line, err := json.Marshal(req)
	if err != nil {
		p.t.Fatalf("marshal %s: %v", method, err)
	}
	if _, err := p.in.Write(append(line, '\n')); err != nil {
		p.t.Fatalf("write %s: %v", method, err)
	}

	// Skip notifications; the reply to this request is the next message with an id.
	for {
		raw, err := p.out.ReadBytes('\n')
		if err != nil {
			p.t.Fatalf("read reply to %s: %v", method, err)
		}
		var msg struct {
			ID     *int             `json:"id"`
			Result json.RawMessage  `json:"result"`
			Error  *json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			p.t.Fatalf("decode reply to %s: %v (%s)", method, err, raw)
		}
		if msg.ID == nil {
			continue
		}
		return msg.Result, msg.Error
	}
}

func (p *peer) mustCall(method string, params any) json.RawMessage {
	p.t.Helper()
	result, errObj := p.call(method, params)
	if errObj != nil {
		p.t.Fatalf("%s failed: %s", method, *errObj)
	}
	return result
}

func testOptions(prepare func(cwd string) (agent.ExecuteWithToolsOptions, error)) Options {
	if prepare == nil {
		prepare = func(string) (agent.ExecuteWithToolsOptions, error) {
			return agent.ExecuteWithToolsOptions{Model: "test-model", MaxIterations: 10}, nil
		}
	}
	return Options{
		Client:  sdk.New(sdk.Options{Secret: "test-secret"}),
		Prepare: prepare,
		Name:    "zot",
		Version: "test",
	}
}

func TestInitializeAdvertisesAgent(t *testing.T) {
	p := newPeer(t, testOptions(nil))

	raw := p.mustCall("initialize", map[string]any{
		"protocolVersion":    acpsdk.ProtocolVersionNumber,
		"clientCapabilities": map[string]any{},
	})

	var resp acpsdk.InitializeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}

	if resp.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		t.Errorf("protocolVersion = %d, want %d", resp.ProtocolVersion, acpsdk.ProtocolVersionNumber)
	}
	if resp.AgentInfo == nil || resp.AgentInfo.Name != "zot" {
		t.Errorf("agentInfo = %+v, want name zot", resp.AgentInfo)
	}
	// Clients decide whether to offer session/close from this capability.
	if resp.AgentCapabilities.SessionCapabilities.Close == nil {
		t.Error("session close capability should be advertised")
	}
}

func TestSessionLifecycle(t *testing.T) {
	dir := t.TempDir()

	var prepared []string
	p := newPeer(t, testOptions(func(cwd string) (agent.ExecuteWithToolsOptions, error) {
		prepared = append(prepared, cwd)
		return agent.ExecuteWithToolsOptions{Model: "test-model", MaxIterations: 10}, nil
	}))

	p.mustCall("initialize", map[string]any{"protocolVersion": acpsdk.ProtocolVersionNumber, "clientCapabilities": map[string]any{}})

	raw := p.mustCall("session/new", map[string]any{"cwd": dir, "mcpServers": []any{}})
	var created acpsdk.NewSessionResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode session/new response: %v", err)
	}
	if created.SessionId == "" {
		t.Fatal("session/new returned no session id")
	}

	// Project context is resolved per session, against the client's directory.
	if len(prepared) != 1 || prepared[0] != dir {
		t.Errorf("prepare called with %v, want [%s]", prepared, dir)
	}

	p.mustCall("session/close", map[string]any{"sessionId": string(created.SessionId)})

	if _, errObj := p.call("session/close", map[string]any{"sessionId": string(created.SessionId)}); errObj == nil {
		t.Error("closing a session twice should fail")
	}
}

func TestNewSessionRejectsBadCwd(t *testing.T) {
	p := newPeer(t, testOptions(nil))
	p.mustCall("initialize", map[string]any{"protocolVersion": acpsdk.ProtocolVersionNumber, "clientCapabilities": map[string]any{}})

	tests := []struct {
		name string
		cwd  string
	}{
		{name: "relative", cwd: "./somewhere"},
		{name: "missing", cwd: filepath.Join(t.TempDir(), "nope")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, errObj := p.call("session/new", map[string]any{"cwd": tt.cwd, "mcpServers": []any{}}); errObj == nil {
				t.Errorf("session/new with a %s cwd should fail", tt.name)
			}
		})
	}
}

func TestNewSessionSurfacesPrepareError(t *testing.T) {
	p := newPeer(t, testOptions(func(string) (agent.ExecuteWithToolsOptions, error) {
		return agent.ExecuteWithToolsOptions{}, fmt.Errorf("bad AGENT.md")
	}))
	p.mustCall("initialize", map[string]any{"protocolVersion": acpsdk.ProtocolVersionNumber, "clientCapabilities": map[string]any{}})

	if _, errObj := p.call("session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}}); errObj == nil {
		t.Error("a failing prepare should fail session/new")
	}
}

func TestPromptRejectsUnknownSession(t *testing.T) {
	p := newPeer(t, testOptions(nil))
	p.mustCall("initialize", map[string]any{"protocolVersion": acpsdk.ProtocolVersionNumber, "clientCapabilities": map[string]any{}})

	_, errObj := p.call("session/prompt", map[string]any{
		"sessionId": "zot_nope",
		"prompt":    []any{map[string]any{"type": "text", "text": "hello"}},
	})
	if errObj == nil {
		t.Error("prompting an unknown session should fail")
	}
}

func TestPromptRejectsEmptyPrompt(t *testing.T) {
	p := newPeer(t, testOptions(nil))
	p.mustCall("initialize", map[string]any{"protocolVersion": acpsdk.ProtocolVersionNumber, "clientCapabilities": map[string]any{}})

	raw := p.mustCall("session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	var created acpsdk.NewSessionResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode session/new response: %v", err)
	}

	_, errObj := p.call("session/prompt", map[string]any{
		"sessionId": string(created.SessionId),
		"prompt":    []any{map[string]any{"type": "text", "text": "   "}},
	})
	if errObj == nil {
		t.Error("a prompt with no text should fail")
	}
}

func TestAuthenticateIsNotOffered(t *testing.T) {
	p := newPeer(t, testOptions(nil))
	p.mustCall("initialize", map[string]any{"protocolVersion": acpsdk.ProtocolVersionNumber, "clientCapabilities": map[string]any{}})

	if _, errObj := p.call("authenticate", map[string]any{"methodId": "whatever"}); errObj == nil {
		t.Error("authenticate should be reported as unsupported")
	}
}
