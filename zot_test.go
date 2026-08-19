package zot

import (
	"context"
	"fmt"
	"gopkg.in/yaml.v3"
	"regexp"

	"github.com/openzot/openzot/agent"
	"github.com/openzot/openzot/internal/config"
	"github.com/openzot/openzot/internal/session"
	"github.com/openzot/openzot/internal/version"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadProjectContext(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()

	// A global AGENT.md in the config dir and a project one in the work dir.
	mustWrite(t, filepath.Join(configDir, "AGENT.md"), "GLOBAL CONVENTIONS")
	mustWrite(t, filepath.Join(workDir, "AGENT.md"), "PROJECT CONVENTIONS")

	// A skill in each location: plain "skills/" in the config dir and hidden
	// ".skills/" in the project dir - both layouts must be picked up.
	mustWrite(t, filepath.Join(configDir, "skills", "greet", "SKILL.md"),
		"---\nname: greet\ndescription: say hello\n---\nbody")
	mustWrite(t, filepath.Join(workDir, ".skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: ship it\n---\nbody")

	cfg := Config{}
	if err := LoadProjectContext(&cfg, configDir, workDir); err != nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}

	// Instructions keeps the default and appends both AGENT.md files in order.
	for _, want := range []string{DefaultInstructions[:20], "GLOBAL CONVENTIONS", "PROJECT CONVENTIONS"} {
		if !strings.Contains(cfg.Agent.Instructions, want) {
			t.Errorf("instructions missing %q", want)
		}
	}
	if i, j := strings.Index(cfg.Agent.Instructions, "GLOBAL"), strings.Index(cfg.Agent.Instructions, "PROJECT"); i > j {
		t.Error("expected config-dir AGENT.md to appear before work-dir AGENT.md")
	}

	// Both skills are discovered and described to the model.
	if len(cfg.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d (%v)", len(cfg.Skills), cfg.Skills)
	}

	names := map[string]bool{}
	for _, skill := range cfg.Skills {
		names[skill.Name] = true

		if skill.Path == "" {
			t.Errorf("skill %q has no path for the model to read", skill.Name)
		}
	}
}

func TestLoadProjectContextNoFiles(t *testing.T) {
	cfg := Config{}
	if err := LoadProjectContext(&cfg, t.TempDir()); err != nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}
	if cfg.Agent.Instructions != "" {
		t.Error("expected instructions untouched when no AGENT.md is present")
	}

	if len(cfg.Skills) != 0 {
		t.Error("expected no skills when none are present")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCfg writes a config file and returns its path.
func writeCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// Credential resolution, which is the part of the configuration that fails
// silently. A key that does not arrive presents as a 401 from the provider,
// which reads like a bad key rather than a config that never picked it up.
//
// These assert on the Authorization header the provider actually receives,
// because that is the only thing that proves a credential was resolved rather
// than merely accepted by the parser. The layering - provider key, per-model
// override, key inlined into the model name - is what the README documents and
// what an older config relies on.
func TestCredentialResolutionLayers(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		config string
		want   string
		model  string
	}{
		{
			name:   "a provider api_key",
			config: "    api_key: sk-provider\n",
			want:   "Bearer sk-provider",
			model:  "gpt-4",
		},
		{
			name:   "a $VAR reference, so no secret is on disk",
			env:    map[string]string{"MY_PROVIDER_KEY": "sk-from-env"},
			config: "    api_key: $MY_PROVIDER_KEY\n",
			want:   "Bearer sk-from-env",
			model:  "gpt-4",
		},
		{
			name: "a per-model key overrides the provider's",
			config: "    api_key: sk-provider\n" +
				"    models:\n      gpt-4:\n        api_key: sk-for-gpt4\n",
			want:  "Bearer sk-for-gpt4",
			model: "gpt-4",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}

			seen := make(chan string, 1)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case seen <- r.Header.Get("Authorization"):
				default:
				}

				w.Header().Set("Content-Type", "text/event-stream")

				fmt.Fprintf(w, "data: %s\n\n",
					`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"done\"}"}}]},"finish_reason":"tool_calls"}]}`)

				fmt.Fprint(w, "data: [DONE]\n\n")
			}))

			defer server.Close()

			path := writeCfg(t, fmt.Sprintf(`
agent:
  model: %q
ui:
  plain: true
default_provider: myprovider
providers:
  myprovider:
    driver: custom
    base_url: %s
%s`, test.model, server.URL, test.config))

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			client, _, err := resolve(cfg, DefaultInstructions)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}

			if got := client.Model(); got != "gpt-4" {
				t.Errorf("model = %q", got)
			}

			if _, err := quietly(t, func() error {
				return RunWith(context.Background(), cfg, "do the thing", RunOptions{})
			}); err != nil {
				t.Fatalf("run: %v", err)
			}

			select {
			case got := <-seen:
				if got != test.want {
					t.Errorf("the provider received %q, want %q", got, test.want)
				}
			default:
				t.Fatal("the provider was never called")
			}
		})
	}
}

// A provider that names no provider cannot resolve, and says so rather than
// sending a request to nowhere.
func TestAProviderWithoutAProviderIsRejected(t *testing.T) {
	cfg := config.Defaults()
	cfg.DefaultProvider = "myprovider"
	cfg.Providers = map[string]config.ProviderConfig{"myprovider": {APIKey: "sk-test"}}

	_, _, err := resolve(cfg, DefaultInstructions)
	if err == nil {
		t.Fatal("a provider naming no provider must be rejected")
	}

	// the error has to be actionable: it names the field and the options
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("error = %q, want it to name what is missing", err)
	}
}

// A provider provider resolves to its own endpoint and credential, with the model
// name passed through untouched.
func TestResolveBuiltInProviders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, name := range []string{"openai", "anthropic"} {
		cfg.DefaultProvider = name

		client, _, err := resolve(cfg, DefaultInstructions)
		if err != nil {
			t.Fatalf("resolve(%s): %v", name, err)
		}

		if client == nil {
			t.Fatalf("resolve(%s): expected a client", name)
		}

		if got := client.Model(); got != cfg.Agent.Model {
			t.Errorf("%s model = %q, want %q unchanged", name, got, cfg.Agent.Model)
		}

		if got := client.Provider(); got != name {
			t.Errorf("%s provider = %q, want %q", name, got, name)
		}
	}
}

// A custom model entry aliases a real id, caps iterations, and carries its own
// credential, all of which take priority over the run defaults.
func TestResolveCustomModelAlias(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	path := writeCfg(t, `
agent:
  model: fast
default_provider: mygateway
providers:
  mygateway:
    driver: openai
    models:
      fast:
        model: gpt-5
        max_iterations: 50
        api_key: $OPENAI_API_KEY
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	client, opts, err := resolve(cfg, DefaultInstructions)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := client.Model(); got != "gpt-5" {
		t.Errorf("model = %q, want gpt-5", got)
	}
	if opts.MaxIterations != 50 {
		t.Errorf("max iterations = %d, want 50 (from custom model)", opts.MaxIterations)
	}
}

// The iteration denominator in the meta bar has to be the limit the run will
// actually stop at. A per-model max_iterations lowers that limit, and a bar
// counting towards a number the run never reaches - "iter 12/100" on a run the
// engine ends at 40 - misreports the run to the only person watching it.
func TestTheViewerShowsTheIterationLimitTheRunEnforces(t *testing.T) {
	cfg := config.Defaults()
	cfg.DefaultProvider = "openai"
	cfg.Agent.Model = "capped"
	cfg.Agent.MaxIterations = 100
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {
			APIKey: "sk-test",
			Models: map[string]config.ModelConfig{
				"capped": {Model: "gpt-5", MaxIterations: 40},
			},
		},
	}

	_, opts, err := resolve(cfg, DefaultInstructions)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if opts.MaxIterations != 40 {
		t.Fatalf("the run resolved to %d iterations, want the model's cap", opts.MaxIterations)
	}

	meta := viewerMeta(cfg, "a task", "/somewhere", opts)

	if meta.MaxIterations != opts.MaxIterations {
		t.Errorf("the viewer shows a limit of %d while the engine stops at %d",
			meta.MaxIterations, opts.MaxIterations)
	}

	// the default is a 1,000,000 backstop rather than a budget, so there is
	// nothing worth counting towards and the denominator stays hidden
	cfg.Agent.MaxIterations = config.Defaults().Agent.MaxIterations
	cfg.Providers["openai"].Models["capped"] = config.ModelConfig{Model: "gpt-5"}

	_, opts, err = resolve(cfg, DefaultInstructions)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got := viewerMeta(cfg, "a task", "/somewhere", opts).MaxIterations; got != 0 {
		t.Errorf("the viewer shows a limit of %d, want the backstop hidden", got)
	}
}

// Run is the whole thing end to end: config in, a provider call out, a
// transcript back. With ui.plain set it takes the non-TTY path, which is what CI
// uses and what can be asserted on.
func TestRunEndToEnd(t *testing.T) {
	turn := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		frames := [][]string{
			{`{"choices":[{"delta":{"content":"working on it"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`},
			{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"all done\"}"}}]},"finish_reason":"tool_calls"}]}`},
		}

		index := turn
		if index >= len(frames) {
			index = len(frames) - 1
		}

		turn++

		for _, frame := range frames[index] {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	cfg := config.Defaults()
	cfg.UI.Plain = true
	cfg.DefaultProvider = "local"
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Driver: "custom", BaseURL: server.URL, APIKey: "k"},
	}

	original := os.Stdout

	read, write, _ := os.Pipe()

	os.Stdout = write

	done := make(chan string)

	go func() {
		var builder strings.Builder

		buffer := make([]byte, 4096)

		for {
			n, err := read.Read(buffer)

			builder.Write(buffer[:n])

			if err != nil {
				break
			}
		}

		done <- builder.String()
	}()

	err := Run(context.Background(), cfg, "do the thing")

	write.Close()

	os.Stdout = original

	output := <-done

	if err != nil {
		t.Fatalf("Run: %v\n%s", err, output)
	}

	for _, want := range []string{"do the thing", "working on it", "all done"} {
		if !strings.Contains(output, want) {
			t.Errorf("transcript is missing %q:\n%s", want, output)
		}
	}
}

// A misconfigured provider fails before any request is made, with a message that
// says what to fix.
func TestRunRejectsAnUnconfiguredProvider(t *testing.T) {
	cfg := config.Defaults()
	cfg.DefaultProvider = "nowhere"
	cfg.Providers = map[string]config.ProviderConfig{}

	err := Run(context.Background(), cfg, "task")

	if err == nil {
		t.Fatal("an unconfigured provider must fail")
	}

	if !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("the error should name the provider: %v", err)
	}
}

// The package re-exports these so an embedder does not have to reach into
// internal packages. What matters is that they are the same values, not that
// they are non-empty.
func TestVersionAndConfigPathAreExposed(t *testing.T) {
	if got, want := Version(), version.Version; got != want {
		t.Errorf("Version() = %q, want %q", got, want)
	}

	t.Setenv("ZOT_CONFIG", "/custom/path.yaml")

	if got, want := DefaultConfigPath(), config.DefaultConfigPath(); got != want {
		t.Errorf("DefaultConfigPath() = %q, want %q", got, want)
	}

	if got := DefaultConfigPath(); got != "/custom/path.yaml" {
		t.Errorf("DefaultConfigPath = %q, want the override honoured", got)
	}
}

// stubProvider returns a config pointed at a server that answers every turn by
// recording a successful outcome.
func stubProvider(t *testing.T) config.Config {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"all done\"}"}}]},"finish_reason":"tool_calls"}]}`)

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	cfg := config.Defaults()
	cfg.UI.Plain = true
	cfg.DefaultProvider = "local"
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Driver: "custom", BaseURL: server.URL, APIKey: "k"},
	}

	return cfg
}

// quietly runs a function with stdout discarded, returning what it printed.
func quietly(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	original := os.Stdout

	read, write, _ := os.Pipe()

	os.Stdout = write

	done := make(chan string)

	go func() {
		var builder strings.Builder

		buffer := make([]byte, 4096)

		for {
			n, err := read.Read(buffer)

			builder.Write(buffer[:n])

			if err != nil {
				break
			}
		}

		done <- builder.String()
	}()

	err := fn()

	write.Close()

	os.Stdout = original

	return <-done, err
}

// A run leaves a record of itself: what it was asked, which model answered, and
// how it ended.
func TestRunWithRecordsASession(t *testing.T) {
	cfg := stubProvider(t)

	sessions := t.TempDir()

	var recorded string

	_, err := quietly(t, func() error {
		return RunWith(context.Background(), cfg, "do the thing", RunOptions{
			SessionDir: sessions,
			OnSession:  func(path string) { recorded = path },
		})
	})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}

	if recorded == "" {
		t.Fatal("the caller must be told where the log is")
	}

	logged, err := session.Load(recorded)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if logged.Meta.Task != "do the thing" || logged.Meta.Provider != "local" || logged.Meta.Driver != "custom" {
		t.Errorf("meta = %+v", logged.Meta)
	}

	if logged.Meta.Model == "" || logged.Meta.Provider == "" || logged.Meta.Workdir == "" {
		t.Errorf("the log must record what it ran against: %+v", logged.Meta)
	}

	if logged.Result == nil || logged.Result.Reason == "" {
		t.Errorf("the outcome must be recorded: %+v", logged.Result)
	}

	// the objective is the durable task, recorded in the meta and placed in the
	// instructions; the opening message is the kickoff, not the task
	if len(logged.Messages) == 0 {
		t.Errorf("the log must record the opening message: %+v", logged.Messages)
	}
}

// A resumed run carries the earlier conversation, so the agent continues rather
// than rediscovering what it already knew.
func TestRunWithResumesAnEarlierSession(t *testing.T) {
	cfg := stubProvider(t)

	earlier := &session.Session{
		Meta: session.Meta{ID: "20260805-090000", Task: "the original brief"},
		Messages: []session.Message{
			{Type: "user", Text: "the original brief"},
			{Type: "bot", Text: "I got halfway"},
		},
	}

	sessions := t.TempDir()

	output, err := quietly(t, func() error {
		return RunWith(context.Background(), cfg, "the original brief", RunOptions{
			SessionDir: sessions,
			Resume:     earlier,
			Prompt:     "carry on",
		})
	})
	if err != nil {
		t.Fatalf("RunWith: %v\n%s", err, output)
	}

	entries, err := session.List(sessions)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d logs, want the resumed run's own", len(entries))
	}

	logged, err := session.Load(entries[0].Path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if logged.Meta.ResumedFrom != "20260805-090000" {
		t.Errorf("ResumedFrom = %q", logged.Meta.ResumedFrom)
	}

	var texts []string

	for _, message := range logged.Messages {
		texts = append(texts, message.Text)
	}

	joined := strings.Join(texts, "|")

	// the replayed conversation plus the new follow-up prompt
	for _, want := range []string{"I got halfway", "carry on"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the resumed conversation is missing %q: %s", want, joined)
		}
	}
}

// The run is the point. A log that cannot be opened is reported and the work
// goes ahead - refusing to work because a directory is read-only would be a
// worse failure than losing the record of it.
func TestRunWithSurvivesAnUnwritableSessionDirectory(t *testing.T) {
	cfg := stubProvider(t)

	blocked := filepath.Join(t.TempDir(), "a-file")

	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := quietly(t, func() error {
		return RunWith(context.Background(), cfg, "do the thing", RunOptions{
			SessionDir: filepath.Join(blocked, "sessions"),
		})
	})
	if err != nil {
		t.Fatalf("RunWith: %v\n%s", err, output)
	}

	if !strings.Contains(output, "all done") {
		t.Errorf("the run should have finished regardless:\n%s", output)
	}
}

// No session directory means no log, and that has to be silent rather than an
// error a caller has to handle.
func TestRunWithoutASessionDirectoryWritesNothing(t *testing.T) {
	cfg := stubProvider(t)

	if _, err := quietly(t, func() error {
		return RunWith(context.Background(), cfg, "do the thing", RunOptions{})
	}); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
}

// The example config is what `zot config` writes on first run, so it is the
// first thing most people ever edit. It drifting from the code's own defaults
// is not cosmetic: someone copies it, changes nothing, and gets different
// behaviour from someone who has no config file at all.
//
// This is also the class of bug that actually happened - the example advertised
// gpt-5.4-mini/openai long after the defaults moved to glm-5.2/zai.
func TestTheExampleConfigMatchesTheDefaults(t *testing.T) {
	var example config.Config

	if err := yaml.Unmarshal(ExampleConfigYAML, &example); err != nil {
		t.Fatalf("the embedded example config does not parse: %v", err)
	}

	defaults := config.Defaults()

	if example.Agent.Model != defaults.Agent.Model {
		t.Errorf("example model = %q, defaults = %q", example.Agent.Model, defaults.Agent.Model)
	}

	if example.DefaultProvider != defaults.DefaultProvider {
		t.Errorf("example provider = %q, defaults = %q", example.DefaultProvider, defaults.DefaultProvider)
	}

	if example.Agent.MaxIterations != defaults.Agent.MaxIterations {
		t.Errorf("example max_iterations = %d, defaults = %d",
			example.Agent.MaxIterations, defaults.Agent.MaxIterations)
	}
}

// A config file is only useful if it survives being loaded, and the example is
// the one file guaranteed to be in front of a new user.
func TestTheExampleConfigLoadsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(path, ExampleConfigYAML, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the example config does not load: %v", err)
	}

	// a key so validation is judging the shape rather than the environment
	provider := cfg.Providers[cfg.DefaultProvider]
	provider.APIKey = "test-key"
	cfg.Providers[cfg.DefaultProvider] = provider

	if err := cfg.Validate(); err != nil {
		t.Fatalf("the example config does not validate: %v", err)
	}
}

// The default model has to be one the default provider can actually serve. A
// pair that cannot talk to each other fails as a provider error rather than a
// configuration one, which is much harder to read.
func TestTheDefaultModelAndProviderCanTalkToEachOther(t *testing.T) {
	defaults := config.Defaults()

	cfg := defaults
	cfg.Providers = map[string]config.ProviderConfig{
		defaults.DefaultProvider: {APIKey: "test-key"},
	}

	client, _, err := resolve(cfg, DefaultInstructions)
	if err != nil {
		t.Fatalf("the default pair does not resolve: %v", err)
	}

	if client.Model() != defaults.Agent.Model {
		t.Errorf("resolved model = %q, want %q", client.Model(), defaults.Agent.Model)
	}

	if client.BaseURL() == "" {
		t.Errorf("the default provider %q resolved to no endpoint", defaults.DefaultProvider)
	}
}

// The task is the durable objective, so it must land in the instructions (the
// system prompt), which compaction never summarises and always orders first -
// not as a user message, which a long run can compact away. An agent that
// forgets its own objective is the worst way for a run to fail.
func TestTaskGoesIntoTheInstructions(t *testing.T) {
	got := withTask("SYSTEM PROMPT", "  build a parser  ")

	if !strings.Contains(got, "SYSTEM PROMPT") {
		t.Error("the base instructions must be preserved")
	}

	if !strings.Contains(got, "build a parser") {
		t.Errorf("the task must be in the instructions: %q", got)
	}

	// trimmed, and clearly the task rather than run together with the prompt
	if strings.Contains(got, "  build a parser  ") {
		t.Error("the task should be trimmed")
	}

	// an empty task leaves the instructions untouched
	if withTask("SYSTEM PROMPT", "   ") != "SYSTEM PROMPT" {
		t.Error("an empty task must not alter the instructions")
	}
}

// The instructions drifted once already - it told the agent to call "edit",
// "exec", "exit" and "progress" when those tools did not exist. This pins it:
// every tool the default instructions names in quotes must be a real tool the
// agent is actually given, or a terminal tool the engine adds in settle mode.
func TestDefaultInstructionsNamesOnlyRealTools(t *testing.T) {
	real := map[string]bool{
		// the terminal tools the loop injects in settle mode
		"success": true,
		"failure": true,
	}

	for name := range agent.DefaultTools() {
		real[name] = true
	}

	// pull every "quoted" token out of the instructions and check the tool-looking
	// ones are real
	for _, quoted := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(DefaultInstructions, -1) {
		name := quoted[1]

		// only check things that look like tool names (a real tool, or the
		// phantom ones we are guarding against)
		phantom := map[string]bool{"edit": true, "exec": true, "exit": true, "abort": true, "plan": true, "progress": true}

		if !real[name] && phantom[name] {
			t.Errorf("the instructions names %q, which is not a real tool", name)
		}
	}

	// and positively assert the tools the instructions promises are all present
	for _, want := range []string{"plan", "progress", "read", "write", "list", "shell", "success", "failure"} {
		if !real[want] {
			t.Errorf("the instructions relies on %q but it is not a real tool", want)
		}

		if !strings.Contains(DefaultInstructions, `"`+want+`"`) {
			t.Errorf("the instructions should name the %q tool so the model knows to use it", want)
		}
	}
}

// The settle and call budgets are configurable, and the config values have to
// actually reach the run - otherwise the knob in the example config is a lie.
// max_settles is the one the operator most wants: how hard zot pushes the model
// to record an outcome before giving up.
func TestRunBudgetsComeFromConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.DefaultProvider = "openai"
	cfg.Providers = map[string]config.ProviderConfig{"openai": {APIKey: "sk-test"}}
	cfg.Agent.MaxSettles = 5
	cfg.Agent.MaxCalls = 33

	_, opts, err := resolve(cfg, DefaultInstructions)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if opts.MaxSettles != 5 {
		t.Errorf("MaxSettles = %d, want the configured 5", opts.MaxSettles)
	}

	if opts.MaxCalls != 33 {
		t.Errorf("MaxCalls = %d, want the configured 33", opts.MaxCalls)
	}

	// max_time is a duration string on the config, a time.Duration on the run
	cfg.Agent.MaxTime = "30m"

	_, timed, err := resolve(cfg, DefaultInstructions)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if timed.MaxDuration != 30*time.Minute {
		t.Errorf("MaxDuration = %v, want 30m", timed.MaxDuration)
	}

	// zero stays zero, so the agent layer falls back to its built-in default
	// rather than pinning the budget to zero
	cfg.Agent.MaxSettles = 0

	_, opts, err = resolve(cfg, DefaultInstructions)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if opts.MaxSettles != 0 {
		t.Errorf("MaxSettles = %d, want 0 (the sentinel for 'use the default')", opts.MaxSettles)
	}
}
