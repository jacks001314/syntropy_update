// Package blob 实现内容寻址的制品存储。
//
// 布局：<root>/<sha256 前两位>/<sha256>。写入先落临时文件、边写边算 sha256、
// fsync 后再原子改名，避免读到半截文件。
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Store 是内容寻址 blob 仓库。
type Store struct {
	root string
}

// ErrInvalidDigest 表示 digest 不是 64 位小写十六进制。
var ErrInvalidDigest = errors.New("blob: invalid sha256 digest")

// New 创建（或打开）blob 仓库。
func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("blob: empty root")
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("blob: create root %s: %w", root, err)
	}
	return &Store{root: root}, nil
}

// Root 返回仓库根目录。
func (s *Store) Root() string { return s.root }

// ValidateDigest 校验 digest 形态。
func ValidateDigest(digest string) error {
	if len(digest) != 64 {
		return ErrInvalidDigest
	}
	for _, r := range digest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ErrInvalidDigest
		}
	}
	return nil
}

// Path 返回 digest 对应的绝对路径。
func (s *Store) Path(digest string) string {
	return filepath.Join(s.root, digest[:2], digest)
}

// PutResult 是一次写入的结果。
type PutResult struct {
	SHA256 string
	Size   int64
}

// Put 读取 r 并落盘，返回其 sha256 与字节数。重复写入同一内容不会重复占用空间。
func (s *Store) Put(r io.Reader) (PutResult, error) {
	tempDir := filepath.Join(s.root, "tmp")
	if err := os.MkdirAll(tempDir, 0o750); err != nil {
		return PutResult{}, fmt.Errorf("blob: create temp dir: %w", err)
	}
	temp, err := os.CreateTemp(tempDir, "upload-*")
	if err != nil {
		return PutResult{}, fmt.Errorf("blob: create temp file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hasher), r)
	if err != nil {
		return PutResult{}, fmt.Errorf("blob: write temp file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return PutResult{}, fmt.Errorf("blob: fsync temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return PutResult{}, fmt.Errorf("blob: close temp file: %w", err)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	finalPath := s.Path(digest)
	if _, err := os.Stat(finalPath); err == nil {
		return PutResult{SHA256: digest, Size: size}, nil
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		return PutResult{}, fmt.Errorf("blob: create shard dir: %w", err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return PutResult{}, fmt.Errorf("blob: commit blob: %w", err)
	}
	return PutResult{SHA256: digest, Size: size}, nil
}

// Open 打开 digest 对应的文件，调用方负责关闭。
func (s *Store) Open(digest string) (*os.File, os.FileInfo, error) {
	if err := ValidateDigest(digest); err != nil {
		return nil, nil, err
	}
	file, err := os.Open(s.Path(digest))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

// Stat 返回 digest 对应文件的信息。
func (s *Store) Stat(digest string) (os.FileInfo, error) {
	if err := ValidateDigest(digest); err != nil {
		return nil, err
	}
	return os.Stat(s.Path(digest))
}

// Has 判断 digest 是否已存在。
func (s *Store) Has(digest string) bool {
	if err := ValidateDigest(digest); err != nil {
		return false
	}
	info, err := os.Stat(s.Path(digest))
	return err == nil && info.Mode().IsRegular()
}
