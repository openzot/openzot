package worker

import (
	"bytes"
	"compress/gzip"
	"testing"
)

// The embedded artifact must expand to the exact executable bytes; corrupt or
// empty build output must never be handed to a compute provider.
func TestDecodeWorkerArtifact(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte("ELF worker executable"))
	_ = writer.Close()

	decoded, err := decode(compressed.Bytes())
	if err != nil || string(decoded) != "ELF worker executable" {
		t.Fatalf("decode = %q, %v", decoded, err)
	}
	if _, err := decode([]byte("not gzip")); err == nil {
		t.Fatal("decode accepted a corrupt artifact")
	}
}

func TestArtifactNameRejectsNonLinuxWorker(t *testing.T) {
	if _, err := artifactName("darwin/arm64"); err == nil {
		t.Fatal("artifactName accepted a host binary for a Linux sandbox")
	}
}
