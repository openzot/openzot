package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openzot/openzot/internal/provider"
)

// toolAt builds one call of a multi-call turn. The shared tool() helper pins
// the delta index to 0, which merges two calls into one malformed set.
func toolAt(index int, id, name, arguments string) string {
	return fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":%d,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`,
		index, id, name, arguments,
	)
}

func testImage(origin string) provider.Image {
	image := provider.NewImage([]byte("bytes of "+origin), "image/png", 800, 600)
	image.Origin = origin

	return image
}

func TestResultTextReadsAToolResultAsItsText(t *testing.T) {
	activity := &Activity{
		Kind:   ActivityResponse,
		Result: ToolResult{Text: "attached shot.png (image/png, 800x600)", Images: []provider.Image{testImage("shot.png")}},
	}

	if got := activity.ResultText(); got != "attached shot.png (image/png, 800x600)" {
		t.Errorf("ResultText = %q; a tool result carrying images must read as its text, not as encoded JSON", got)
	}

	if strings.Contains(activity.ResultText(), "base64") {
		t.Error("image bytes must never reach the model as a tool result")
	}
}

func TestAttachmentMessageIsEmptyWithoutImages(t *testing.T) {
	if _, ok := attachmentMessage(nil); ok {
		t.Error("a turn that attached nothing must not append a message")
	}
}

func TestAttachmentMessageDescribesEachImageSoItStandsAlone(t *testing.T) {
	message, ok := attachmentMessage([]attachment{
		{tool: "view", call: "c1", image: testImage("/tmp/one.png")},
		{tool: "view", call: "c2", image: testImage("/tmp/two.png")},
	})

	if !ok {
		t.Fatal("images must produce a message")
	}

	if message.Type != TypeAttachment {
		t.Errorf("type = %q, want %q", message.Type, TypeAttachment)
	}

	if len(message.Images) != 2 {
		t.Fatalf("got %d images, want both", len(message.Images))
	}

	// the text is what remains when compaction drops the images or a blob goes
	// missing, so both files and their shape have to be named in it
	for _, want := range []string{"/tmp/one.png", "/tmp/two.png", "image/png", "800x600"} {
		if !strings.Contains(message.Text, want) {
			t.Errorf("text %q does not mention %q", message.Text, want)
		}
	}
}

func TestAttachmentMessageNamesTheToolWhenAnImageHasNoOrigin(t *testing.T) {
	image := provider.NewImage([]byte("x"), "image/webp", 0, 0)

	message, _ := attachmentMessage([]attachment{{tool: "render", call: "c1", image: image}})

	if !strings.Contains(message.Text, "render") {
		t.Errorf("text = %q, want the tool named when the image has no path", message.Text)
	}

	if strings.Contains(message.Text, "0x0") {
		t.Errorf("text = %q, want no dimensions rather than a meaningless 0x0", message.Text)
	}
}

func TestAttachmentMessageCapsOneTurnAndSaysSo(t *testing.T) {
	var attached []attachment

	for range maxAttachmentsPerTurn + 3 {
		attached = append(attached, attachment{tool: "view", call: "c", image: testImage("/tmp/a.png")})
	}

	message, _ := attachmentMessage(attached)

	if len(message.Images) != maxAttachmentsPerTurn {
		t.Errorf("attached %d images, want the cap of %d", len(message.Images), maxAttachmentsPerTurn)
	}

	if !strings.Contains(message.Text, "3 further image(s) not attached") {
		t.Errorf("text = %q, want the dropped images named so the loss is visible", message.Text)
	}
}

func TestConvertSendsAnAttachmentAsAUserMessageWithItsImages(t *testing.T) {
	image := testImage("/tmp/shot.png")

	converted := toChatMessages([]Message{
		{Type: TypeAttachment, Text: "Attached: /tmp/shot.png (image/png, 800x600)", Images: []provider.Image{image}},
	})

	if len(converted) != 1 {
		t.Fatalf("got %d messages, want one", len(converted))
	}

	// the only role an OpenAI-compatible endpoint accepts image parts on
	if converted[0].Role != provider.RoleUser {
		t.Errorf("role = %q, want %q", converted[0].Role, provider.RoleUser)
	}

	if len(converted[0].Images) != 1 {
		t.Fatalf("images did not survive conversion")
	}

	if converted[0].Images[0].Digest != image.Digest {
		t.Error("the image that reaches the wire must be the one that was attached")
	}
}

func TestDispatchAttachesImagesAfterEveryToolResultOfTheTurn(t *testing.T) {
	tools := map[string]ToolDefinition{
		"view": {
			Name:       "view",
			Parameters: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				return ToolResult{Text: "attached", Images: []provider.Image{testImage("/tmp/shot.png")}}, nil
			},
		},
		"note": {
			Name:       "note",
			Parameters: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				return "noted", nil
			},
		},
	}

	result := run(t, Options{
		Client: stub(t,
			[]string{toolAt(0, "c1", "view", `{"path":"/tmp/shot.png"}`), toolAt(1, "c2", "note", `{}`)},
			[]string{text("seen"), stop()},
		),
		Tools:    tools,
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})

	var (
		lastResult  = -1
		attachedAt  = -1
		attachments int
	)

	for index, message := range result.Messages {
		if message.Activity != nil && message.Activity.Kind == ActivityResponse {
			lastResult = index
		}

		if message.Type == TypeAttachment {
			attachedAt = index
			attachments++
		}
	}

	if attachments != 1 {
		t.Fatalf("got %d attachment messages, want exactly one for the turn", attachments)
	}

	// a user message wedged between two tool results invalidates the turn, so
	// the attachment has to come after the last of them
	if attachedAt < lastResult {
		t.Errorf("attachment at %d comes before the last tool result at %d", attachedAt, lastResult)
	}

	if len(result.Messages[attachedAt].Images) != 1 {
		t.Error("the attachment message must carry the image the tool produced")
	}
}

func TestDispatchAttachesNothingForOrdinaryTools(t *testing.T) {
	tools := map[string]ToolDefinition{
		"note": {
			Name:       "note",
			Parameters: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				return "noted", nil
			},
		},
	}

	result := run(t, Options{
		Client: stub(t,
			[]string{tool("c1", "note", `{}`)},
			[]string{text("ok"), stop()},
		),
		Tools:    tools,
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})

	for _, message := range result.Messages {
		if message.Type == TypeAttachment {
			t.Fatal("a tool that returned text must not produce an attachment message")
		}
	}
}
