// Package acp serves zot over the Agent Client Protocol (ACP), so an ACP client
// - an editor, or a harness such as Buzz's buzz-acp - can drive zot as a coding
// agent over stdio.
//
// The protocol is JSON-RPC on stdin/stdout, so while the server runs nothing
// else in the process may write to stdout; diagnostics belong on stderr.
//
// zot's normal mode is one autonomous task rendered in a read-only viewer. ACP
// mode keeps the same agent loop but makes it conversational: every
// session/prompt is one turn against the session's accumulated history, and the
// SDK's file and shell tools operate inside the session's working directory.
package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/chatbotkit/go-sdk/agent"
	"github.com/chatbotkit/go-sdk/sdk"
)

// Options configures the ACP server.
type Options struct {
	// Client is the ChatBotKit client every turn runs against.
	Client *sdk.Client
	// Prepare builds the agent options for a session rooted at cwd. It is called
	// once per session/new, so each workspace picks up its own AGENT.md and
	// skills. The returned options must leave Messages empty - the server owns
	// conversation history.
	Prepare func(cwd string) (agent.ExecuteWithToolsOptions, error)
	// Name and Version identify this agent to the client during initialize.
	Name    string
	Version string
	// Log receives human-readable diagnostics. It must not write to stdout,
	// which belongs to the protocol. A nil Log discards them.
	Log func(format string, args ...any)
}

// Serve runs the ACP server over stdin/stdout until the client disconnects or
// ctx is cancelled.
func Serve(ctx context.Context, opts Options) error {
	return serve(ctx, opts, os.Stdin, os.Stdout)
}

// serve runs the protocol over an arbitrary pair of streams.
func serve(ctx context.Context, opts Options, in io.Reader, out io.Writer) error {
	if opts.Client == nil {
		return fmt.Errorf("acp: no ChatBotKit client")
	}
	if opts.Prepare == nil {
		return fmt.Errorf("acp: no prepare function")
	}

	s := &server{
		opts:     opts,
		ready:    make(chan struct{}),
		sessions: map[acpsdk.SessionId]*session{},
		turn:     make(chan struct{}, 1),
	}

	// The connection starts reading as soon as it is constructed, so handlers
	// wait on ready rather than racing the assignment below.
	conn := acpsdk.NewAgentSideConnection(s, out, in)
	s.conn = conn
	close(s.ready)

	s.logf("serving ACP (%s %s)", opts.Name, opts.Version)

	select {
	case <-conn.Done():
	case <-ctx.Done():
	}
	return nil
}

// server implements acpsdk.Agent on top of the ChatBotKit agent loop.
type server struct {
	opts Options

	// ready is closed once conn is set; handlers must not use conn before then.
	ready chan struct{}
	conn  *acpsdk.AgentSideConnection

	mu       sync.Mutex
	sessions map[acpsdk.SessionId]*session

	// turn serializes prompts across sessions. The SDK's file and shell tools
	// resolve relative paths against the process working directory, so a turn
	// owns that directory for its whole duration. ACP clients that drive one
	// prompt at a time - buzz-acp does - never contend for it.
	turn chan struct{}
}

var _ acpsdk.Agent = (*server)(nil)

// session is one ACP conversation: a fixed workspace plus the history of every
// turn taken in it.
type session struct {
	cwd  string
	opts agent.ExecuteWithToolsOptions

	// mu guards history against overlapping turns in the same session.
	mu      sync.Mutex
	history []agent.Message
}

func (s *server) logf(format string, args ...any) {
	if s.opts.Log != nil {
		s.opts.Log(format, args...)
	}
}

// connection returns the client connection once it is safe to use.
func (s *server) connection(ctx context.Context) (*acpsdk.AgentSideConnection, error) {
	select {
	case <-s.ready:
		return s.conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Initialize reports zot's identity and capabilities. zot brings its own file
// and shell tools, so it needs nothing from the client's filesystem or terminal
// capabilities.
func (s *server) Initialize(_ context.Context, params acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	s.logf("initialize: client protocol version %d", params.ProtocolVersion)

	return acpsdk.InitializeResponse{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		AgentInfo: &acpsdk.Implementation{
			Name:    s.opts.Name,
			Version: s.opts.Version,
		},
		AgentCapabilities: acpsdk.AgentCapabilities{
			SessionCapabilities: acpsdk.SessionCapabilities{
				Close: &acpsdk.SessionCloseCapabilities{},
			},
		},
	}, nil
}

// NewSession creates a conversation rooted at params.Cwd. Project context -
// AGENT.md and skills - is resolved per session, so a client driving several
// repositories gets the right instructions in each.
func (s *server) NewSession(_ context.Context, params acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	cwd := params.Cwd
	if !filepath.IsAbs(cwd) {
		return acpsdk.NewSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{
			"error": fmt.Sprintf("cwd must be an absolute path, got %q", cwd),
		})
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return acpsdk.NewSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{
			"error": fmt.Sprintf("cwd %q is not a readable directory", cwd),
		})
	}

	// zot runs its own tools in-process and has no MCP client, so any servers
	// the client offers go unused. Say so rather than failing the session: the
	// agent is still fully functional without them.
	if len(params.McpServers) > 0 {
		s.logf("ignoring %d MCP server(s) offered by the client (zot has no MCP support): %s",
			len(params.McpServers), strings.Join(mcpNames(params.McpServers), ", "))
	}

	opts, err := s.opts.Prepare(cwd)
	if err != nil {
		return acpsdk.NewSessionResponse{}, fmt.Errorf("prepare session for %s: %w", cwd, err)
	}

	id, err := newSessionID()
	if err != nil {
		return acpsdk.NewSessionResponse{}, err
	}

	s.mu.Lock()
	s.sessions[id] = &session{cwd: cwd, opts: opts}
	s.mu.Unlock()

	s.logf("session %s created in %s", id, cwd)

	return acpsdk.NewSessionResponse{SessionId: id}, nil
}

// Prompt runs one turn: the client's message is appended to the session history
// and the agent works it to completion with zot's tools, streaming its activity
// back as session updates.
func (s *server) Prompt(ctx context.Context, params acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	sess := s.session(params.SessionId)
	if sess == nil {
		return acpsdk.PromptResponse{}, acpsdk.NewInvalidParams(map[string]any{
			"error": fmt.Sprintf("unknown session %q", params.SessionId),
		})
	}

	text := promptText(params.Prompt)
	if text == "" {
		return acpsdk.PromptResponse{}, acpsdk.NewInvalidParams(map[string]any{
			"error": "prompt has no text content",
		})
	}

	conn, err := s.connection(ctx)
	if err != nil {
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonCancelled}, nil
	}

	// Take the process working directory for the duration of the turn. A cancel
	// while queued ends the turn rather than making the client wait.
	select {
	case s.turn <- struct{}{}:
	case <-ctx.Done():
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonCancelled}, nil
	}
	defer func() { <-s.turn }()

	restore, err := enterDir(sess.cwd)
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}
	defer restore()

	sess.mu.Lock()
	defer sess.mu.Unlock()

	opts := sess.opts
	opts.Messages = append(append([]agent.Message{}, sess.history...), agent.Message{Type: "user", Text: text})

	st := newStream(conn, params.SessionId, opts.MaxIterations)
	events, errs := agent.ExecuteWithTools(ctx, s.opts.Client, opts)

	var updateErr error
	for ev := range events {
		if updateErr != nil {
			// The connection is gone; drain the stream so the agent goroutine
			// can finish instead of blocking on an unread channel.
			continue
		}
		if err := st.handle(ctx, ev); err != nil {
			updateErr = err
		}
	}

	if err := <-errs; err != nil {
		if ctx.Err() != nil {
			return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonCancelled}, nil
		}
		return acpsdk.PromptResponse{}, err
	}
	if ctx.Err() != nil {
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonCancelled}, nil
	}
	if updateErr != nil {
		return acpsdk.PromptResponse{}, updateErr
	}

	// Mirror how the SDK builds history inside a run: the messages we sent plus
	// every message the server appended along the way.
	sess.history = append(opts.Messages, st.messages...)

	return acpsdk.PromptResponse{StopReason: st.stopReason()}, nil
}

// Cancel implements session/cancel. The connection has already cancelled the
// in-flight prompt's context by the time this runs, and Prompt turns that into
// a "cancelled" stop reason, so there is nothing left to do here.
func (s *server) Cancel(_ context.Context, params acpsdk.CancelNotification) error {
	s.logf("session %s cancelled", params.SessionId)
	return nil
}

// CloseSession frees a session. Long-lived clients rotate sessions (buzz-acp
// does on !rotate), so without this the map would grow for the life of the
// process.
func (s *server) CloseSession(_ context.Context, params acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	s.mu.Lock()
	_, ok := s.sessions[params.SessionId]
	delete(s.sessions, params.SessionId)
	s.mu.Unlock()

	if !ok {
		return acpsdk.CloseSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{
			"error": fmt.Sprintf("unknown session %q", params.SessionId),
		})
	}

	s.logf("session %s closed", params.SessionId)

	return acpsdk.CloseSessionResponse{}, nil
}

// Authenticate is not used: zot authenticates to its backend with its own API
// secret and advertises no ACP auth methods.
func (s *server) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodAuthenticate)
}

func (s *server) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodLogout)
}

func (s *server) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return acpsdk.ListSessionsResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionList)
}

func (s *server) ResumeSession(context.Context, acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	return acpsdk.ResumeSessionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionResume)
}

func (s *server) SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	return acpsdk.SetSessionConfigOptionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionSetConfigOption)
}

func (s *server) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionSetMode)
}

func (s *server) session(id acpsdk.SessionId) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// promptText flattens the client's content blocks into the single user message
// the agent loop takes. Text blocks carry the message; resource links are named
// so the agent can read them with its own tools.
func promptText(blocks []acpsdk.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			if t := strings.TrimSpace(b.Text.Text); t != "" {
				parts = append(parts, t)
			}
		case b.ResourceLink != nil:
			name := b.ResourceLink.Name
			if name == "" {
				name = b.ResourceLink.Uri
			}
			parts = append(parts, fmt.Sprintf("[resource] %s: %s", name, b.ResourceLink.Uri))
		case b.Resource != nil:
			if b.Resource.Resource.TextResourceContents != nil {
				r := b.Resource.Resource.TextResourceContents
				parts = append(parts, fmt.Sprintf("[resource] %s\n%s", r.Uri, r.Text))
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// mcpNames names the MCP servers a client offered, whichever transport each one
// uses, for the log line that explains they go unused.
func mcpNames(servers []acpsdk.McpServer) []string {
	names := make([]string, 0, len(servers))
	for _, server := range servers {
		switch {
		case server.Stdio != nil:
			names = append(names, server.Stdio.Name)
		case server.Http != nil:
			names = append(names, server.Http.Name)
		case server.Sse != nil:
			names = append(names, server.Sse.Name)
		case server.Acp != nil:
			names = append(names, server.Acp.Name)
		}
	}
	return names
}

// enterDir makes dir the process working directory and returns a function that
// restores the previous one.
func enterDir(dir string) (func(), error) {
	prev, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot read the working directory: %w", err)
	}
	if err := os.Chdir(dir); err != nil {
		return nil, fmt.Errorf("cannot enter session directory %q: %w", dir, err)
	}
	return func() { _ = os.Chdir(prev) }, nil
}

func newSessionID() (acpsdk.SessionId, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cannot generate a session id: %w", err)
	}
	return acpsdk.SessionId("zot_" + hex.EncodeToString(b[:])), nil
}
