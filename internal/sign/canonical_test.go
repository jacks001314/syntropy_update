package sign

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type signVectorFile struct {
	Note         string       `json:"note"`
	PublicKeyPEM string       `json:"publicKeyPem"`
	Vectors      []signVector `json:"vectors"`
}

type signVector struct {
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
	Canonical string          `json:"canonical"`
	Signature string          `json:"signature"`
}

// TestCanonicalizeMatchesDesktopVectors 用 Node 生成的向量锁定与 update-service.ts
// 的字节一致性。向量由 tests/vectors/generate.mjs 产出。
func TestCanonicalizeMatchesDesktopVectors(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "tests", "vectors", "sign-vectors.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var file signVectorFile
	if err := json.Unmarshal(content, &file); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(file.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	publicKey, err := ParsePublicKeyPEM([]byte(file.PublicKeyPEM))
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	for _, vector := range file.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			canonical, err := Canonicalize(vector.Payload)
			if err != nil {
				t.Fatalf("canonicalize: %v", err)
			}
			if canonical != vector.Canonical {
				t.Fatalf("canonical mismatch\n got: %s\nwant: %s", canonical, vector.Canonical)
			}
			verified, err := VerifyEnvelope(publicKey, Envelope{Payload: vector.Payload, Signature: vector.Signature})
			if err != nil {
				t.Fatalf("verify envelope: %v", err)
			}
			if verified != vector.Canonical {
				t.Fatalf("verified canonical mismatch")
			}
		})
	}
}

func TestVerifyEnvelopeRejectsTamperedPayload(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "tests", "vectors", "sign-vectors.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var file signVectorFile
	if err := json.Unmarshal(content, &file); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	publicKey, err := ParsePublicKeyPEM([]byte(file.PublicKeyPEM))
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(file.Vectors[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	payload["version"] = "9.9.9"
	tampered, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal tampered: %v", err)
	}
	if _, err := VerifyEnvelope(publicKey, Envelope{Payload: tampered, Signature: file.Vectors[0].Signature}); err == nil {
		t.Fatal("expected tampered payload to fail verification")
	}
}

func TestSignEnvelopeRoundTrip(t *testing.T) {
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	payload := map[string]any{"channel": "stable", "size": 42, "url": "https://app.syntropy-lab.com/blobs/x"}
	encoded, err := SignEnvelope(keys.Private, payload)
	if err != nil {
		t.Fatalf("sign envelope: %v", err)
	}
	var envelope Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	canonical, err := VerifyEnvelope(keys.Public, envelope)
	if err != nil {
		t.Fatalf("verify envelope: %v", err)
	}
	want := `{"channel":"stable","size":42,"url":"https://app.syntropy-lab.com/blobs/x"}`
	if canonical != want {
		t.Fatalf("canonical = %s, want %s", canonical, want)
	}
}

func TestWriteAndLoadPrivateKey(t *testing.T) {
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "signing.pem")
	if err := WritePrivateKeyFile(path, keys.Private); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	loaded, err := LoadPrivateKeyPEM(path)
	if err != nil {
		t.Fatalf("load private key: %v", err)
	}
	if !loaded.Equal(keys.Private) {
		t.Fatal("loaded private key differs")
	}
}
