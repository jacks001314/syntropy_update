// Package config 定义并加载升级服务配置。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config 是升级服务的运行时配置，字段与 conf/update.json.tpl 对应。
type Config struct {
	Addr           string   `json:"addr"`
	PublicBaseURL  string   `json:"publicBaseURL"`
	DataDir        string   `json:"dataDir"`
	LogDir         string   `json:"logDir"`
	LogLevel       string   `json:"logLevel"`
	SigningKeyFile string   `json:"signingKeyFile"`
	AdminTokens    []string `json:"adminTokens"`
	ClientTokens   []string `json:"clientTokens"`
	// RequireDeviceTokens 为 true 时，上报类客户端接口（/v1/plan、库存、忽略、升级结果）
	// 一律要求客户端令牌，哪怕 clientTokens 没配。默认 false：没配令牌就放行，与老配置
	// 的行为一致；deploy/install.sh 新建配置时写 true，新装即默认鉴权。
	RequireDeviceTokens bool `json:"requireDeviceTokens"`
	// RequireReadTokens 为 true 时，读取类客户端接口（清单、制品、分发定义）也要求令牌。
	// 默认 false：thin 安装器首启时本机还没有任何配置，拉清单与制品必须能匿名走通；打开它
	// 就得先把令牌发到每一台目标端，否则它们连引导都做不了。
	RequireReadTokens bool     `json:"requireReadTokens"`
	Channels          []string `json:"channels"`
	DefaultChannel    string   `json:"defaultChannel"`
	ManifestTTLHours  int      `json:"manifestTTLHours"`
	DBFile            string   `json:"dbFile"`
	BlobDir           string   `json:"blobDir"`
	// TLS：tlsDomains 非空时另起一个 HTTPS 监听（默认 :443），证书用 ACME 自动签发。
	// 80 端口常常进不来，autocert 会先试 TLS-ALPN-01，这条路只要 443 能进就能签发。
	TLSAddr     string   `json:"tlsAddr"`
	TLSDomains  []string `json:"tlsDomains"`
	TLSEmail    string   `json:"tlsEmail"`
	TLSCacheDir string   `json:"tlsCacheDir"`
}

// Default 返回带默认值的配置。
func Default() Config {
	return Config{
		Addr:             "0.0.0.0:8443",
		PublicBaseURL:    "https://app.syntropy-lab.com",
		DataDir:          "data",
		LogDir:           "logs",
		LogLevel:         "info",
		SigningKeyFile:   filepath.Join("keys", "update-signing.pem"),
		Channels:         []string{"alpha", "beta", "stable"},
		DefaultChannel:   "stable",
		ManifestTTLHours: 168,
		TLSAddr:          ":443",
	}
}

// Load 读取配置文件并补齐默认值。
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := json.Unmarshal(content, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) normalize() error {
	if strings.TrimSpace(c.Addr) == "" {
		c.Addr = "0.0.0.0:8443"
	}
	if strings.TrimSpace(c.PublicBaseURL) == "" {
		c.PublicBaseURL = "https://app.syntropy-lab.com"
	}
	c.PublicBaseURL = strings.TrimRight(c.PublicBaseURL, "/")
	if strings.TrimSpace(c.DataDir) == "" {
		c.DataDir = "data"
	}
	if strings.TrimSpace(c.LogDir) == "" {
		c.LogDir = "logs"
	}
	if strings.TrimSpace(c.LogLevel) == "" {
		c.LogLevel = "info"
	}
	if strings.TrimSpace(c.SigningKeyFile) == "" {
		c.SigningKeyFile = filepath.Join("keys", "update-signing.pem")
	}
	if strings.TrimSpace(c.DBFile) == "" {
		c.DBFile = filepath.Join(c.DataDir, "update.db")
	}
	if strings.TrimSpace(c.BlobDir) == "" {
		c.BlobDir = filepath.Join(c.DataDir, "blobs")
	}
	if strings.TrimSpace(c.TLSAddr) == "" {
		c.TLSAddr = ":443"
	}
	if strings.TrimSpace(c.TLSCacheDir) == "" {
		c.TLSCacheDir = filepath.Join(c.DataDir, "acme")
	}
	c.TLSDomains = normalizeDomains(c.TLSDomains)
	if len(c.Channels) == 0 {
		c.Channels = []string{"alpha", "beta", "stable"}
	}
	if strings.TrimSpace(c.DefaultChannel) == "" {
		c.DefaultChannel = "stable"
	}
	if c.ManifestTTLHours <= 0 {
		c.ManifestTTLHours = 168
	}
	return nil
}

// normalizeDomains 去掉空白与重复并转小写：ACME 的 HostPolicy 按主机名精确匹配，
// 重复项和大小写差异只会让配置看起来对、实际对不上。
func normalizeDomains(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	domains := make([]string, 0, len(values))
	for _, value := range values {
		domain := strings.ToLower(strings.TrimSpace(value))
		if domain == "" {
			continue
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return domains
}

// IsChannel 判断是否是已配置的渠道。
func (c Config) IsChannel(name string) bool {
	for _, channel := range c.Channels {
		if channel == name {
			return true
		}
	}
	return false
}
