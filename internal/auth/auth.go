// Package auth 实现基于 Bearer Token 的简单鉴权。
package auth

import (
	"net/http"
	"strings"
)

// Tokens 持有管理端与客户端令牌集合。
//
// 客户端接口分两档，两侧的默认值不同，因为「谁在调用」不同：
//   - 读取（清单 / 制品 / 分发定义）：调用方是刚装好的 thin 安装器，本机还没有任何配置，
//     默认匿名放行；要收紧得显式打开 requireReadTokens，并先把令牌发到每一台目标端。
//   - 上报（计划 / 库存 / 忽略 / 升级结果）：调用方是已经在跑的目标端，代表一台设备写数据，
//     配了令牌就校验；requireDeviceTokens 让「没配令牌」也能拒绝匿名写入。
type Tokens struct {
	admin  map[string]struct{}
	client map[string]struct{}
	// requireRead 是「读取类也要令牌」的开关。
	requireRead bool
	// requireDevice 是「没配客户端令牌也必须带令牌」的开关。
	requireDevice bool
}

// New 构造令牌集合。requireReadTokens 让读取类也校验令牌；requireDeviceTokens 让上报类
// 即使没配令牌也拒绝匿名请求。
func New(adminTokens, clientTokens []string, requireReadTokens, requireDeviceTokens bool) Tokens {
	return Tokens{
		admin:         toSet(adminTokens),
		client:        toSet(clientTokens),
		requireRead:   requireReadTokens,
		requireDevice: requireDeviceTokens,
	}
}

func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	return set
}

// AdminEnabled 表示是否配置了管理令牌。
func (t Tokens) AdminEnabled() bool { return len(t.admin) > 0 }

// ClientTokensConfigured 表示是否配了客户端令牌（读取 / 上报据此校验，见下面两个方法）。
func (t Tokens) ClientTokensConfigured() bool { return len(t.client) > 0 }

// ReadRequired 表示读取类客户端接口是否强制鉴权。
func (t Tokens) ReadRequired() bool { return t.requireRead }

// DeviceRequired 表示上报类客户端接口（计划、库存、忽略、升级结果）是否强制鉴权：
// 配了客户端令牌，或显式打开 requireDeviceTokens 时为真。
func (t Tokens) DeviceRequired() bool { return len(t.client) > 0 || t.requireDevice }

// Token 从 Authorization 或 X-Api-Key 头提取令牌。
func Token(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header != "" {
		if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
			return strings.TrimSpace(header[7:])
		}
		return header
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// AllowAdmin 判断请求是否通过管理端鉴权。未配置管理令牌时拒绝所有管理请求。
func (t Tokens) AllowAdmin(r *http.Request) bool {
	if len(t.admin) == 0 {
		return false
	}
	_, ok := t.admin[Token(r)]
	return ok
}

// AllowClient 判断请求是否通过读取类客户端鉴权。默认放行：thin 安装器首启时本机还没有
// 任何配置，拉分发清单与制品必须能在没有令牌的前提下走通。打开 requireReadTokens 后按
// 令牌校验——那时没配令牌就是「谁都进不来」，而不是「谁都能进」。
func (t Tokens) AllowClient(r *http.Request) bool {
	if !t.requireRead {
		return true
	}
	if len(t.client) == 0 {
		return false
	}
	_, ok := t.client[Token(r)]
	return ok
}

// AllowDevice 判断请求是否通过上报类客户端鉴权。比 AllowClient 严：没配令牌时由
// requireDeviceTokens 决定，而不是无条件放行——「没配令牌」和「谁都能写」是两回事。
func (t Tokens) AllowDevice(r *http.Request) bool {
	if len(t.client) == 0 {
		return !t.requireDevice
	}
	_, ok := t.client[Token(r)]
	return ok
}
