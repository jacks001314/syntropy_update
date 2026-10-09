// Package policy 实现分发策略的纯逻辑：版本比较、确定性灰度分桶、维护窗口与目标匹配。
package policy

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// CompareVersions 比较两个 MAJOR.MINOR.PATCH 版本，返回 -1/0/1。
func CompareVersions(left, right string) (int, error) {
	leftParts, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	for index := 0; index < 3; index++ {
		switch {
		case leftParts[index] < rightParts[index]:
			return -1, nil
		case leftParts[index] > rightParts[index]:
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersion(value string) ([3]int, error) {
	var parts [3]int
	segments := strings.Split(strings.TrimSpace(value), ".")
	if len(segments) != 3 {
		return parts, fmt.Errorf("policy: version %q must be MAJOR.MINOR.PATCH", value)
	}
	for index, segment := range segments {
		number, err := strconv.Atoi(segment)
		if err != nil || number < 0 {
			return parts, fmt.Errorf("policy: version %q has invalid segment %q", value, segment)
		}
		parts[index] = number
	}
	return parts, nil
}

// Bucket 返回 0..99 的确定性分桶值：同一 (target, component, release) 永远落在同一桶。
func Bucket(targetID, component, releaseID string) int {
	digest := sha256.Sum256([]byte(targetID + "|" + component + "|" + releaseID))
	return int(binary.BigEndian.Uint32(digest[:4]) % 100)
}

// WithinWindow 判断 now（UTC 的时分）是否落在 "HH:MM-HH:MM" 窗口内；支持跨零点。
// 空窗口表示不限时间。
func WithinWindow(window string, now time.Time) (bool, error) {
	trimmed := strings.TrimSpace(window)
	if trimmed == "" {
		return true, nil
	}
	segments := strings.Split(trimmed, "-")
	if len(segments) != 2 {
		return false, fmt.Errorf("policy: window %q must be HH:MM-HH:MM", window)
	}
	start, err := parseClock(segments[0])
	if err != nil {
		return false, err
	}
	end, err := parseClock(segments[1])
	if err != nil {
		return false, err
	}
	current := now.UTC().Hour()*60 + now.UTC().Minute()
	if start == end {
		return true, nil
	}
	if start < end {
		return current >= start && current < end, nil
	}
	// 跨零点：例如 22:00-04:00。
	return current >= start || current < end, nil
}

func parseClock(value string) (int, error) {
	segments := strings.Split(strings.TrimSpace(value), ":")
	if len(segments) != 2 {
		return 0, fmt.Errorf("policy: clock %q must be HH:MM", value)
	}
	hour, err := strconv.Atoi(segments[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, fmt.Errorf("policy: clock %q has invalid hour", value)
	}
	minute, err := strconv.Atoi(segments[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("policy: clock %q has invalid minute", value)
	}
	return hour*60 + minute, nil
}

// MatchTarget 判断 targetID 是否命中任一 glob 模式；模式为空表示全部命中。
func MatchTarget(patterns []string, targetID string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if matched, err := path.Match(trimmed, targetID); err == nil && matched {
			return true
		}
	}
	return false
}
