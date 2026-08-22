package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openzot/openzot/agent"
	"github.com/openzot/openzot/internal/provider"
)

func openWriter(t *testing.T) (*Writer, string) {
	t.Helper()

	dir := t.TempDir()

	writer, err := Create(dir, "20260822-120000", Meta{Task: "look at a screenshot"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Cleanup(func() { writer.Close() })

	return writer, dir
}

func TestStoreImageWritesTheBytesBesideTheLog(t *testing.T) {
	writer, dir := openWriter(t)

	image := provider.NewImage([]byte("the png bytes"), "image/png", 100, 50)

	stored, err := writer.StoreImage(image)
	if err != nil {
		t.Fatalf("StoreImage: %v", err)
	}

	if stored.Source != provider.SourceBlob {
		t.Errorf("source = %q, want %q", stored.Source, provider.SourceBlob)
	}

	if len(stored.Bytes) != 0 || stored.Data != "" {
		t.Error("what goes in the log is the shape, not the payload")
	}

	if stored.Digest != image.Digest || stored.Width != 100 {
		t.Error("storing must not change how the image is described")
	}

	blobs, err := os.ReadDir(filepath.Join(dir, "20260822-120000"+BlobSuffix))
	if err != nil {
		t.Fatalf("blob directory: %v", err)
	}

	if len(blobs) != 1 {
		t.Fatalf("got %d blobs, want one", len(blobs))
	}

	if !strings.HasSuffix(blobs[0].Name(), ".png") {
		t.Errorf("blob %q should be named for its type", blobs[0].Name())
	}

	written, err := os.ReadFile(filepath.Join(dir, "20260822-120000"+BlobSuffix, blobs[0].Name()))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}

	if string(written) != "the png bytes" {
		t.Errorf("blob holds %q, want the image's own bytes", written)
	}
}

func TestStoreImageKeepsOneCopyOfTheSameImage(t *testing.T) {
	writer, dir := openWriter(t)

	for range 3 {
		if _, err := writer.StoreImage(provider.NewImage([]byte("same screenshot"), "image/png", 10, 10)); err != nil {
			t.Fatalf("StoreImage: %v", err)
		}
	}

	blobs, err := os.ReadDir(filepath.Join(dir, "20260822-120000"+BlobSuffix))
	if err != nil {
		t.Fatalf("blob directory: %v", err)
	}

	if len(blobs) != 1 {
		t.Errorf("got %d blobs, want the same bytes stored once", len(blobs))
	}
}

func TestARunThatAttachesNothingLeavesNoBlobDirectory(t *testing.T) {
	writer, dir := openWriter(t)

	if err := writer.Message(Message{Type: "user", Text: "no images here"}); err != nil {
		t.Fatalf("Message: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "20260822-120000"+BlobSuffix)); !os.IsNotExist(err) {
		t.Error("the blob directory must be created on first use, not on every run")
	}
}

func TestRecordedMessageKeepsTheImageOutOfTheLogLine(t *testing.T) {
	writer, dir := openWriter(t)

	recorder := NewRecorder(writer)

	image := provider.NewImage([]byte("bytes that must not be inlined"), "image/png", 800, 600)
	image.Origin = "/tmp/shot.png"

	err := recorder.RecordMessage(agent.Message{
		Type:   agent.TypeAttachment,
		Text:   "Attached: /tmp/shot.png (image/png, 800x600)",
		Images: []agent.Image{image},
	})
	if err != nil {
		t.Fatalf("RecordMessage: %v", err)
	}

	log, err := os.ReadFile(filepath.Join(dir, "20260822-120000.jsonl"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	if strings.Contains(string(log), "bytes that must not be inlined") {
		t.Error("image bytes must not be written into the log line")
	}

	if !strings.Contains(string(log), image.Digest) {
		t.Error("the record must name the blob it describes")
	}

	// and the line is still one parseable record
	var record Record

	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		var candidate Record

		if err := json.Unmarshal([]byte(line), &candidate); err != nil {
			t.Fatalf("log line is not valid JSON: %v", err)
		}

		if candidate.Kind == KindMessage {
			record = candidate
		}
	}

	if record.Message == nil || len(record.Message.Images) != 1 {
		t.Fatal("the message record must carry the image's shape")
	}

	if record.Message.Images[0].Width != 800 {
		t.Errorf("recorded width = %d, want 800", record.Message.Images[0].Width)
	}
}

func TestResumeRehydratesAnImageFromItsBlob(t *testing.T) {
	writer, dir := openWriter(t)

	recorder := NewRecorder(writer)

	image := provider.NewImage([]byte("original bytes"), "image/png", 20, 10)
	image.Origin = "/tmp/shot.png"

	if err := recorder.RecordMessage(agent.Message{
		Type:   agent.TypeAttachment,
		Text:   "Attached: /tmp/shot.png",
		Images: []agent.Image{image},
	}); err != nil {
		t.Fatalf("RecordMessage: %v", err)
	}

	writer.Close()

	loaded, err := Load(filepath.Join(dir, "20260822-120000.jsonl"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	messages := loaded.AgentMessages()

	if len(messages) != 1 || len(messages[0].Images) != 1 {
		t.Fatalf("resumed conversation lost the attachment: %+v", messages)
	}

	restored := messages[0].Images[0]

	if string(restored.Bytes) != "original bytes" {
		t.Errorf("restored bytes = %q, want the image the run actually sent", restored.Bytes)
	}

	if !restored.Ready() {
		t.Error("a rehydrated image must be ready to send again")
	}
}

func TestResumeSurvivesABlobThatIsGone(t *testing.T) {
	writer, dir := openWriter(t)

	recorder := NewRecorder(writer)

	if err := recorder.RecordMessage(agent.Message{
		Type:   agent.TypeAttachment,
		Text:   "Attached: /tmp/shot.png (image/png, 20x10)",
		Images: []agent.Image{provider.NewImage([]byte("original bytes"), "image/png", 20, 10)},
	}); err != nil {
		t.Fatalf("RecordMessage: %v", err)
	}

	writer.Close()

	// logs get pruned, copied and cached; the run must still resume
	if err := os.RemoveAll(filepath.Join(dir, "20260822-120000"+BlobSuffix)); err != nil {
		t.Fatalf("remove blobs: %v", err)
	}

	loaded, err := Load(filepath.Join(dir, "20260822-120000.jsonl"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	messages := loaded.AgentMessages()

	if len(messages) != 1 {
		t.Fatalf("got %d messages, want the attachment kept", len(messages))
	}

	if messages[0].Images[0].Ready() {
		t.Error("an image with no bytes must not claim to be sendable")
	}

	if !strings.Contains(messages[0].Text, "/tmp/shot.png") {
		t.Error("the text must still say what the model was shown")
	}
}

func TestLoadImageRefusesBytesThatDoNotMatchTheDigest(t *testing.T) {
	writer, dir := openWriter(t)

	image := provider.NewImage([]byte("real bytes"), "image/png", 20, 10)

	stored, err := writer.StoreImage(image)
	if err != nil {
		t.Fatalf("StoreImage: %v", err)
	}

	blobs := filepath.Join(dir, "20260822-120000"+BlobSuffix)
	name := blobName(stored)

	if err := os.WriteFile(filepath.Join(blobs, name), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	session := &Session{Blobs: blobs}

	if got := session.LoadImage(stored); got.Ready() {
		t.Error("bytes that do not hash to the recorded digest are not this image")
	}
}

func TestAWriterWithNowhereToPutBlobsKeepsTheImageInline(t *testing.T) {
	writer := &Writer{}

	stored, err := writer.StoreImage(provider.NewImage([]byte("bytes"), "image/png", 4, 4))
	if err != nil {
		t.Fatalf("StoreImage: %v", err)
	}

	if stored.Source != provider.SourceInline || stored.Data == "" {
		t.Error("with no blob directory the bytes must be kept inline rather than lost")
	}

	if len(stored.Bytes) != 0 {
		t.Error("the payload must not be carried twice")
	}
}

func TestSessionReadFromAReaderHasNoBlobsToLoad(t *testing.T) {
	// a log parsed from a pipe has no path behind it, so an image cannot be
	// rehydrated - it must degrade, not panic
	session, err := Read(strings.NewReader(
		`{"kind":"message","at":"` + time.Now().UTC().Format(time.RFC3339) +
			`","message":{"type":"attachment","text":"Attached: shot.png","images":[{"media_type":"image/png","digest":"sha256:abc","source":"blob"}]}}`))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	messages := session.AgentMessages()

	if len(messages) != 1 || len(messages[0].Images) != 1 {
		t.Fatalf("the attachment did not survive parsing: %+v", messages)
	}

	if messages[0].Images[0].Ready() {
		t.Error("an image with no blob directory behind it cannot be ready")
	}
}
