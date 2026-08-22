package agent

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, dir, name string, width, height int) string {
	t.Helper()

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := range height {
		for x := range width {
			canvas.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}

	var buffer bytes.Buffer

	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatalf("encode: %v", err)
	}

	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	return path
}

func TestViewIsOfferedOnlyToAModelThatCanSee(t *testing.T) {
	if _, ok := DefaultTools()["view"]; ok {
		t.Error("the default tool set must not offer view: an uncatalogued model is assumed blind")
	}

	if _, ok := DefaultToolsFor(ToolOptions{})["view"]; ok {
		t.Error("view must be off unless vision is asked for")
	}

	tools := DefaultToolsFor(ToolOptions{Vision: true})

	definition, ok := tools["view"]

	if !ok {
		t.Fatal("a model with vision must be offered view")
	}

	if definition.Handler == nil || definition.Description == "" {
		t.Error("view must be a complete tool definition")
	}

	if _, ok := definition.Parameters["properties"]; !ok {
		t.Error("view must describe its arguments")
	}

	// the rest of the set is unchanged either way
	for _, name := range []string{"read", "write", "list", "shell"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("%s went missing when vision was enabled", name)
		}
	}
}

func TestViewReturnsTheImageAsAnAttachableResult(t *testing.T) {
	path := writePNG(t, t.TempDir(), "shot.png", 120, 90)

	set := toolSet{maxOutput: DefaultMaxToolOutput, vision: true}

	output, err := set.view(t.Context(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("view: %v", err)
	}

	result, ok := output.(ToolResult)

	if !ok {
		t.Fatalf("view returned %T, want a ToolResult the engine can attach", output)
	}

	if len(result.Images) != 1 {
		t.Fatalf("got %d images, want one", len(result.Images))
	}

	got := result.Images[0]

	if got.MediaType != "image/png" {
		t.Errorf("media type = %q", got.MediaType)
	}

	if got.Width != 120 || got.Height != 90 {
		t.Errorf("dimensions = %dx%d, want 120x90", got.Width, got.Height)
	}

	if len(got.Bytes) == 0 {
		t.Error("the image must carry its bytes: the recorder is what decides where they are kept")
	}

	if got.Origin != path {
		t.Errorf("origin = %q, want the path it was read from", got.Origin)
	}

	if got.Digest == "" || got.Tokens == 0 {
		t.Error("an attached image must carry its digest and its estimated cost")
	}

	// the text is what the model reads as the tool result, and what remains if
	// the image is ever dropped
	if !strings.Contains(result.Text, path) || !strings.Contains(result.Text, "120x90") {
		t.Errorf("result text %q must describe the image on its own", result.Text)
	}
}

func TestViewReducesAnOversizeImageAndSaysSo(t *testing.T) {
	path := writePNG(t, t.TempDir(), "huge.png", 2400, 1200)

	set := toolSet{maxOutput: DefaultMaxToolOutput, vision: true}

	output, err := set.view(t.Context(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("view: %v", err)
	}

	result := output.(ToolResult)

	if got := result.Images[0].Width; got != 1568 {
		t.Errorf("width = %d, want the image reduced to the default edge", got)
	}

	if !strings.Contains(result.Text, "reduced") {
		t.Errorf("text = %q, must not imply the model saw the file as it is on disk", result.Text)
	}
}

func TestViewCarriesTheDetailHintOnlyWhenItIsValid(t *testing.T) {
	path := writePNG(t, t.TempDir(), "shot.png", 40, 40)

	set := toolSet{maxOutput: DefaultMaxToolOutput, vision: true}

	output, err := set.view(t.Context(), map[string]any{"path": path, "detail": "low"})
	if err != nil {
		t.Fatalf("view: %v", err)
	}

	if got := output.(ToolResult).Images[0].Detail; got != "low" {
		t.Errorf("detail = %q, want low", got)
	}

	output, err = set.view(t.Context(), map[string]any{"path": path, "detail": "enormous"})
	if err != nil {
		t.Fatalf("view: %v", err)
	}

	if got := output.(ToolResult).Images[0].Detail; got != "" {
		t.Errorf("detail = %q, want an unrecognised hint dropped rather than sent", got)
	}
}

func TestViewRefusesWhatIsNotAnImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")

	if err := os.WriteFile(path, []byte("just text"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	set := toolSet{maxOutput: DefaultMaxToolOutput, vision: true}

	if _, err := set.view(t.Context(), map[string]any{"path": path}); err == nil {
		t.Error("view must refuse a text file rather than send it as an image")
	}

	if _, err := set.view(t.Context(), map[string]any{"path": filepath.Join(dir, "missing.png")}); err == nil {
		t.Error("view must report a missing file")
	}

	if _, err := set.view(t.Context(), map[string]any{}); err == nil {
		t.Error("view must require a path")
	}
}

func TestReadDescribesAnImageInsteadOfSplittingItIntoLines(t *testing.T) {
	path := writePNG(t, t.TempDir(), "shot.png", 64, 32)

	seeing := toolSet{maxOutput: DefaultMaxToolOutput, vision: true}

	output, err := seeing.read(t.Context(), map[string]any{"path": path, "startLine": 1, "endLine": 40})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	got, _ := output.(string)

	if strings.Contains(got, "�") {
		t.Error("read must not return an image as mojibake")
	}

	for _, want := range []string{"image/png", "64x32"} {
		if !strings.Contains(got, want) {
			t.Errorf("read = %q, want it to describe the file (%s)", got, want)
		}
	}

	if !strings.Contains(got, "view") {
		t.Errorf("read = %q, want it to point a seeing model at view", got)
	}

	blind := toolSet{maxOutput: DefaultMaxToolOutput}

	output, err = blind.read(t.Context(), map[string]any{"path": path, "startLine": 1, "endLine": 40})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	got, _ = output.(string)

	if !strings.Contains(got, "cannot be shown images") {
		t.Errorf("read = %q, want a blind model told why it cannot see the file", got)
	}

	if strings.Contains(got, "Use view") {
		t.Error("a blind model must not be pointed at a tool it was never offered")
	}
}
