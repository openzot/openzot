package provider

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewImageDescribesTheBytesItIsGiven(t *testing.T) {
	data := []byte("pretend png")

	image := NewImage(data, "image/png", 800, 600)

	if image.Size != len(data) {
		t.Errorf("Size = %d, want %d", image.Size, len(data))
	}

	if !strings.HasPrefix(image.Digest, "sha256:") {
		t.Errorf("Digest = %q, want a sha256 content address", image.Digest)
	}

	if image.Digest != Digest(data) {
		t.Error("an image's digest must be the digest of its own bytes")
	}

	if image.Tokens <= 0 {
		t.Error("an image must carry a token estimate, or the budget is blind to it")
	}
}

func TestDigestChangesWithTheBytes(t *testing.T) {
	if Digest([]byte("a")) == Digest([]byte("b")) {
		t.Fatal("different bytes must not share a content address")
	}

	if Digest([]byte("same")) != Digest([]byte("same")) {
		t.Error("the same bytes must address the same blob, so a repeated image is stored once")
	}
}

func TestExtensionNamesTheBlobByType(t *testing.T) {
	cases := map[string]string{
		"image/png":                "png",
		"image/jpeg":               "jpg",
		"image/gif":                "gif",
		"image/webp":               "webp",
		"application/octet-stream": "bin",
	}

	for mediaType, want := range cases {
		if got := (Image{MediaType: mediaType}).Extension(); got != want {
			t.Errorf("%s extension = %q, want %q", mediaType, got, want)
		}
	}
}

func TestDataURLCarriesTheEncodedImage(t *testing.T) {
	image := NewImage([]byte{1, 2, 3}, "image/png", 10, 10)

	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{1, 2, 3})

	if got := image.DataURL(); got != want {
		t.Errorf("DataURL = %q, want %q", got, want)
	}

	// a record read back from a log carries base64 rather than bytes
	stored := Image{MediaType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte{1, 2, 3})}

	if stored.DataURL() != want {
		t.Error("an inline image must render the same URL as one with bytes in memory")
	}
}

func TestReadyDistinguishesAnImageWithNoBytes(t *testing.T) {
	if (Image{MediaType: "image/png"}).Ready() {
		t.Error("an image whose blob went missing must not report itself ready to send")
	}

	if !(Image{Bytes: []byte("x")}).Ready() {
		t.Error("an image with bytes is ready")
	}

	if !(Image{Data: "eA=="}).Ready() {
		t.Error("an inline image is ready")
	}
}

func TestEstimateImageTokensGrowsWithArea(t *testing.T) {
	small := EstimateImageTokens(100, 100)
	large := EstimateImageTokens(2000, 2000)

	if small <= 0 {
		t.Fatalf("a measurable image must cost something, got %d", small)
	}

	if large <= small {
		t.Errorf("a larger image must estimate higher: %d vs %d", large, small)
	}

	if unmeasurable := EstimateImageTokens(0, 0); unmeasurable <= 0 {
		t.Error("an unmeasurable image must not be estimated at nothing - the budget would ignore it")
	}
}

func TestChatMessageMarshalsAsAStringWhenItHasNoImages(t *testing.T) {
	encoded, err := json.Marshal(ChatMessage{Role: RoleUser, Content: "plain"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		Content json.RawMessage `json:"content"`
	}

	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if string(decoded.Content) != `"plain"` {
		t.Errorf("content = %s, want the scalar every provider accepts", decoded.Content)
	}
}

func TestChatMessagePromotesContentToPartsWhenItCarriesImages(t *testing.T) {
	message := ChatMessage{
		Role:    RoleUser,
		Content: "look at this",
		Images:  []Image{NewImage([]byte{9, 9}, "image/png", 20, 10)},
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		Content []map[string]any `json:"content"`
	}

	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("content is not an array of parts: %v", err)
	}

	if len(decoded.Content) != 2 {
		t.Fatalf("got %d parts, want the text and the image", len(decoded.Content))
	}

	if decoded.Content[0]["type"] != "text" || decoded.Content[0]["text"] != "look at this" {
		t.Errorf("first part = %v, want the message text", decoded.Content[0])
	}

	if decoded.Content[1]["type"] != "image_url" {
		t.Fatalf("second part = %v, want an image", decoded.Content[1])
	}

	url, _ := decoded.Content[1]["image_url"].(map[string]any)

	if !strings.HasPrefix(url["url"].(string), "data:image/png;base64,") {
		t.Errorf("image url = %v, want a data URL", url["url"])
	}

	if strings.Contains(string(encoded), `"images"`) {
		t.Error("images must reach the wire as content parts, not as a field of their own")
	}
}

func TestChatMessageCarriesTheDetailHintWhenSet(t *testing.T) {
	image := NewImage([]byte{1}, "image/png", 10, 10)
	image.Detail = "low"

	encoded, err := json.Marshal(ChatMessage{Role: RoleUser, Images: []Image{image}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !strings.Contains(string(encoded), `"detail":"low"`) {
		t.Errorf("detail hint did not reach the wire: %s", encoded)
	}
}

func TestChatMessageSkipsAnImageWithNoBytes(t *testing.T) {
	message := ChatMessage{
		Role:    RoleUser,
		Content: "the screenshot described below",
		Images:  []Image{{MediaType: "image/png", Digest: "sha256:gone"}},
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		Content json.RawMessage `json:"content"`
	}

	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if string(decoded.Content) != `"the screenshot described below"` {
		t.Errorf("content = %s; an image whose blob is missing must leave the text alone rather than send an empty part", decoded.Content)
	}
}
