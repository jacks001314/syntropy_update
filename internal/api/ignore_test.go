package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// publishMBot 用管理接口上架一个 mbot 发布——忽略集合必须站在真实发布之上验证。
func publishMBot(t *testing.T, client *http.Client, baseURL, version string) {
	t.Helper()
	adminHeaders := map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}
	componentBody, _ := json.Marshal(map[string]any{"name": "mbot", "kind": "binary", "versioning": "product"})
	response, content := doRequest(t, client, http.MethodPost, baseURL+"/v1/admin/components", componentBody, adminHeaders)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("put component status = %d body = %s", response.StatusCode, content)
	}
	releaseBody, _ := json.Marshal(map[string]any{"component": "mbot", "version": version, "channel": "stable", "publish": true})
	response, content = doRequest(t, client, http.MethodPost, baseURL+"/v1/admin/releases", releaseBody, adminHeaders)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create release status = %d body = %s", response.StatusCode, content)
	}
}

func planItemsFor(t *testing.T, client *http.Client, baseURL, version string) planView {
	t.Helper()
	status, plan := postPlan(t, client, baseURL+"/v1/plan", map[string]any{
		"channel": "stable",
		"targets": []map[string]any{{
			"id":         "node-a",
			"kind":       "node",
			"components": map[string]string{"mbot": version},
		}},
	})
	if status != http.StatusCreated {
		t.Fatalf("plan status = %d", status)
	}
	return plan
}

func TestIgnoreEndpointDrivesThePlan(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	publishMBot(t, client, server.URL, "1.2.0")

	if plan := planItemsFor(t, client, server.URL, "1.1.0"); len(plan.Items) != 1 || plan.Items[0].Action != "upgrade" {
		t.Fatalf("baseline plan = %+v, want one upgrade", plan.Items)
	}

	// 客户端上报忽略：走客户端作用域（本测试没配 clientTokens，所以直接放行）。
	ignoreBody, _ := json.Marshal(map[string]any{"component": "mbot", "version": "1.2.0"})
	response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/targets/node-a/ignores", ignoreBody, map[string]string{"Content-Type": "application/json"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("set ignore status = %d body = %s", response.StatusCode, content)
	}
	var saved struct {
		Component   string `json:"component"`
		TargetID    string `json:"targetId"`
		Version     string `json:"version"`
		UpdatedAtMS int64  `json:"updatedAtMs"`
	}
	if err := json.Unmarshal(content, &saved); err != nil {
		t.Fatalf("unmarshal ignore: %v body = %s", err, content)
	}
	if saved.TargetID != "node-a" || saved.Component != "mbot" || saved.Version != "1.2.0" || saved.UpdatedAtMS == 0 {
		t.Fatalf("saved ignore = %+v", saved)
	}

	plan := planItemsFor(t, client, server.URL, "1.1.0")
	if len(plan.Items) != 1 {
		t.Fatalf("plan after ignore = %+v, the ignored item must still be listed", plan.Items)
	}
	item := plan.Items[0]
	if item.Action != "skip" || item.State != "deferred" || item.To != "1.2.0" {
		t.Fatalf("ignored item = %+v, want skip/deferred on 1.2.0", item)
	}

	// 管理端能看到忽略集合，且必须带令牌。
	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/ignores", nil, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin ignores without token status = %d", response.StatusCode)
	}
	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/ignores?target=node-a", nil,
		map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin ignores status = %d body = %s", response.StatusCode, content)
	}
	var listed struct {
		Ignores []struct {
			TargetID  string `json:"targetId"`
			Component string `json:"component"`
			Version   string `json:"version"`
		} `json:"ignores"`
	}
	if err := json.Unmarshal(content, &listed); err != nil {
		t.Fatalf("unmarshal ignores: %v body = %s", err, content)
	}
	if len(listed.Ignores) != 1 || listed.Ignores[0].Version != "1.2.0" {
		t.Fatalf("listed ignores = %+v", listed.Ignores)
	}

	// 撤销之后该版本重新回到计划里。
	response, content = doRequest(t, client, http.MethodDelete, server.URL+"/v1/targets/node-a/ignores/mbot", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("clear ignore status = %d body = %s", response.StatusCode, content)
	}
	if plan := planItemsFor(t, client, server.URL, "1.1.0"); len(plan.Items) != 1 || plan.Items[0].Action != "upgrade" {
		t.Fatalf("plan after clearing the ignore = %+v, want an upgrade again", plan.Items)
	}
	response, _ = doRequest(t, client, http.MethodDelete, server.URL+"/v1/targets/node-a/ignores/mbot", nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("clearing a missing ignore status = %d, want 404", response.StatusCode)
	}
}

func TestIgnoreEndpointValidatesInput(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	headers := map[string]string{"Content-Type": "application/json"}

	for _, payload := range []map[string]any{
		{"component": "mbot"},
		{"version": "1.2.0"},
		{"component": "mbot", "version": "1.2"},
	} {
		body, _ := json.Marshal(payload)
		response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/targets/node-a/ignores", body, headers)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("payload %+v status = %d body = %s", payload, response.StatusCode, content)
		}
	}
}
