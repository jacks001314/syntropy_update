package policy

import (
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"1.2.0", "1.2.0", 0},
		{"1.2.0", "1.3.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.2.10", "1.2.9", 1},
		{"0.160.0", "0.9.0", 1},
	}
	for _, testCase := range cases {
		got, err := CompareVersions(testCase.left, testCase.right)
		if err != nil {
			t.Fatalf("compare %s/%s: %v", testCase.left, testCase.right, err)
		}
		if got != testCase.want {
			t.Fatalf("compare(%s, %s) = %d, want %d", testCase.left, testCase.right, got, testCase.want)
		}
	}
	if _, err := CompareVersions("1.2", "1.2.0"); err == nil {
		t.Fatal("expected invalid version error")
	}
}

func TestBucketIsDeterministicAndBounded(t *testing.T) {
	first := Bucket("node-a", "nbot", "release-1")
	for index := 0; index < 10; index++ {
		if again := Bucket("node-a", "nbot", "release-1"); again != first {
			t.Fatalf("bucket changed: %d vs %d", again, first)
		}
	}
	if first < 0 || first > 99 {
		t.Fatalf("bucket out of range: %d", first)
	}
	if first == Bucket("node-b", "nbot", "release-1") && first == Bucket("node-a", "nbot", "release-2") {
		t.Log("bucket collision observed (allowed, hash based)")
	}
}

func TestWithinWindow(t *testing.T) {
	cases := []struct {
		window string
		now    string
		want   bool
	}{
		{"", "2026-10-05T03:00:00Z", true},
		{"02:00-05:00", "2026-10-05T03:00:00Z", true},
		{"02:00-05:00", "2026-10-05T05:00:00Z", false},
		{"02:00-05:00", "2026-10-05T01:59:00Z", false},
		{"22:00-04:00", "2026-10-05T23:30:00Z", true},
		{"22:00-04:00", "2026-10-05T02:00:00Z", true},
		{"22:00-04:00", "2026-10-05T05:00:00Z", false},
	}
	for _, testCase := range cases {
		now, err := time.Parse(time.RFC3339, testCase.now)
		if err != nil {
			t.Fatalf("parse %s: %v", testCase.now, err)
		}
		got, err := WithinWindow(testCase.window, now)
		if err != nil {
			t.Fatalf("window %q: %v", testCase.window, err)
		}
		if got != testCase.want {
			t.Fatalf("WithinWindow(%q, %s) = %v, want %v", testCase.window, testCase.now, got, testCase.want)
		}
	}
	if _, err := WithinWindow("bad", time.Now()); err == nil {
		t.Fatal("expected invalid window error")
	}
}

func TestMatchTarget(t *testing.T) {
	if !MatchTarget(nil, "node-a") {
		t.Fatal("empty patterns should match everything")
	}
	if !MatchTarget([]string{"node-*"}, "node-a") {
		t.Fatal("glob should match")
	}
	if MatchTarget([]string{"node-*"}, "master-1") {
		t.Fatal("glob should not match")
	}
	if !MatchTarget([]string{"other", "node-a"}, "node-a") {
		t.Fatal("exact pattern should match")
	}
}
