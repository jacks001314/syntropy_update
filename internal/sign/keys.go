package sign

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

const (
	privateKeyPEMType = "PRIVATE KEY"
	publicKeyPEMType  = "PUBLIC KEY"
)

// Keys 是一对 Ed25519 密钥，用于签发更新清单。
type Keys struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

// GenerateKeys 生成新的 Ed25519 密钥对。
func GenerateKeys() (Keys, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return Keys{Private: private, Public: public}, nil
}

// PrivateKeyPEM 返回 PKCS8 编码的私钥 PEM。
func (k Keys) PrivateKeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k.Private)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: privateKeyPEMType, Bytes: der}), nil
}

// PublicKeyPEM 返回 SPKI 编码的公钥 PEM。
func (k Keys) PublicKeyPEM() ([]byte, error) {
	return MarshalPublicKeyPEM(k.Public)
}

// MarshalPublicKeyPEM 返回 SPKI 编码的公钥 PEM。
func MarshalPublicKeyPEM(public ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMType, Bytes: der}), nil
}

// LoadPrivateKeyPEM 读取 PKCS8 私钥 PEM。
func LoadPrivateKeyPEM(path string) (ed25519.PrivateKey, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key %s: %w", path, err)
	}
	return ParsePrivateKeyPEM(content)
}

// ParsePrivateKeyPEM 解析 PKCS8 私钥 PEM。
func ParsePrivateKeyPEM(content []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, errors.New("private key: no PEM block found")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse pkcs8 private key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key: unexpected type %T", parsed)
	}
	return key, nil
}

// ParsePublicKeyPEM 解析 SPKI 公钥 PEM。
func ParsePublicKeyPEM(content []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, errors.New("public key: no PEM block found")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse pkix public key: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key: unexpected type %T", parsed)
	}
	return key, nil
}

// WritePrivateKeyFile 以 0600 权限写入私钥 PEM。
func WritePrivateKeyFile(path string, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: privateKeyPEMType, Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write private key %s: %w", path, err)
	}
	return nil
}

// Envelope 是签名信封：payload 为原始 JSON，signature 为 Ed25519 的 base64。
//
// 保持 payload 为 json.RawMessage，确保服务端输出与签名输入完全一致。
type Envelope struct {
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}

// SignPayload 对 payload 的规范化 JSON 做 Ed25519 签名。payload 应为 json.Marshal 产物。
func SignPayload(private ed25519.PrivateKey, payload []byte) (string, error) {
	canonical, err := Canonicalize(payload)
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(private, []byte(canonical))
	return base64.StdEncoding.EncodeToString(signature), nil
}

// SignEnvelope 返回序列化后的签名信封。
func SignEnvelope(private ed25519.PrivateKey, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	signature, err := SignPayload(private, raw)
	if err != nil {
		return nil, err
	}
	envelope := Envelope{Payload: raw, Signature: signature}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	return encoded, nil
}

// VerifyEnvelope 校验签名信封，返回 payload 的规范化串。
func VerifyEnvelope(public ed25519.PublicKey, envelope Envelope) (string, error) {
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return "", fmt.Errorf("decode signature: %w", err)
	}
	if len(signature) != ed25519.SignatureSize {
		return "", fmt.Errorf("signature length %d, want %d", len(signature), ed25519.SignatureSize)
	}
	canonical, err := Canonicalize(envelope.Payload)
	if err != nil {
		return "", err
	}
	if !ed25519.Verify(public, []byte(canonical), signature) {
		return "", errors.New("signature verification failed")
	}
	return canonical, nil
}
