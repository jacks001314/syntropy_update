package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestResultEndpointRecordsAndLists 覆盖「升级结果」这条链路：目标端（客户端作用域）上报，
// 管理面（需要令牌）只读浏览。
func TestResultEndpointRecordsAndLists(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	clientHeaders := map[string]string{"Content-Type": "application/json"}

	body, _ := json.Marshal(map[string]any{
		"action":    "upgrade",
		"component": "app-server",
		"from":      "1.0.0",
		"message":   "已把 app-server 换到 1.1.0",
		"planId":    "plan-1",
		"state":     "installed",
		"to":        "1.1.0",
	})
	response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/targets/desktop/results", body, clientHeaders)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("report result status = %d body = %s", response.StatusCode, content)
	}
	var saved struct {
		ID        string `json:"id"`
		TargetID  string `json:"targetId"`
		Component string `json:"component"`
		State     string `json:"state"`
		AtMS      int64  `json:"atMs"`
	}
	if err := json.Unmarshal(content, &saved); err != nil {
		t.Fatalf("unmarshal result: %v body = %s", err, content)
	}
	if saved.ID == "" || saved.TargetID != "desktop" || saved.Component != "app-server" || saved.State != "installed" || saved.AtMS == 0 {
		t.Fatalf("saved result = %+v", saved)
	}

	// 管理面：没令牌一律 401，带令牌能看到刚上报的那条。
	response, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/results", nil, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin results without token status = %d, want 401", response.StatusCode)
	}
	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/results?target=desktop", nil,
		map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin results status = %d body = %s", response.StatusCode, content)
	}
	var listed struct {
		Results []struct {
			Component string `json:"component"`
			TargetID  string `json:"targetId"`
			State     string `json:"state"`
		} `json:"results"`
	}
	if err := json.Unmarshal(content, &listed); err != nil {
		t.Fatalf("unmarshal results: %v body = %s", err, content)
	}
	if len(listed.Results) != 1 || listed.Results[0].Component != "app-server" {
		t.Fatalf("listed results = %+v", listed.Results)
	}

	// 另一个目标的结果不该混进来：
	other, _ := json.Marshal(map[string]any{"component": "nbot", "state": "failed", "message": "重下发超时"})
	if response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/targets/node-a/results", other, clientHeaders); response.StatusCode != http.StatusCreated {
		t.Fatalf("report other target status = %d body = %s", response.StatusCode, content)
	}
	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/results?target=desktop", nil,
		map[string]string{"Authorization": "Bearer admin-secret"})
	if err := json.Unmarshal(content, &listed); err != nil {
		t.Fatalf("unmarshal filtered results: %v", err)
	}
	if len(listed.Results) != 1 {
		t.Fatalf("target filter leaked results: %+v", listed.Results)
	}
}

// TestResultEndpointValidatesInput 钉住输入校验：状态白名单、未知字段、缺少组件都要当场拒绝。
func TestResultEndpointValidatesInput(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	headers := map[string]string{"Content-Type": "application/json"}

	for _, payload := range []map[string]any{
		{"state": "installed"},
		{"component": "app-server"},
		{"component": "app-server", "state": "planned"},
		{"component": "app-server", "state": "installed", "from": "1.0"},
		{"component": "app-server", "state": "installed", "releaseId": "release/../etc"},
		{"component": "app-server", "state": "installed", "unknown": "field"},
	} {
		body, _ := json.Marshal(payload)
		response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/targets/desktop/results", body, headers)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("payload %+v status = %d body = %s", payload, response.StatusCode, content)
		}
	}
}
