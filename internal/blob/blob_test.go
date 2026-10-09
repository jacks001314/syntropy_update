package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
)

func TestPutAndOpen(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	content := []byte("syntropy artifact payload")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	result, err := store.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if result.SHA256 != digest || result.Size != int64(len(content)) {
		t.Fatalf("put result = %+v, want digest %s size %d", result, digest, len(content))
	}
	if !store.Has(digest) {
		t.Fatal("expected blob to exist")
	}

	file, info, err := store.Open(digest)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = file.Close() }()
	read, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(read, content) {
		t.Fatal("blob content mismatch")
	}
	if info.Size() != int64(len(content)) {
		t.Fatalf("blob size = %d, want %d", info.Size(), len(content))
	}
}

func TestPutIsIdempotent(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	content := []byte("same bytes")
	first, err := store.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	second, err := store.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("digest mismatch: %s vs %s", first.SHA256, second.SHA256)
	}
}

func TestInvalidDigestRejected(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	for _, digest := range []string{"", "zz", "../../etc/passwd", "A" + strings.Repeat("a", 63), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		if _, _, err := store.Open(digest); err == nil {
			t.Fatalf("expected %q to be rejected", digest)
		}
	}
}

func TestOpenMissingBlob(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, _, err := store.Open("a" + strings.Repeat("0", 63)); !os.IsNotExist(err) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}
