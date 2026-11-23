package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

type fakeZcodeBrowserProvider struct {
	browsers    []ZcodeBrowser
	tabs        []ZcodeUserTab
	listErr     error
	tabsErr     error
	listScopes  []ZcodeBrowserScope
	tabScopes   []ZcodeBrowserScope
	tabBrowsers []ZcodeBrowser
	listFn      func(ZcodeBrowserScope) ([]ZcodeBrowser, error)
}

func (p *fakeZcodeBrowserProvider) ListBrowsers(_ context.Context, scope ZcodeBrowserScope) ([]ZcodeBrowser, error) {
	p.listScopes = append(p.listScopes, scope)
	if p.listFn != nil {
		return p.listFn(scope)
	}
	return p.browsers, p.listErr
}

func (p *fakeZcodeBrowserProvider) ListUserTabs(_ context.Context, scope ZcodeBrowserScope, browser ZcodeBrowser) ([]ZcodeUserTab, error) {
	p.tabScopes = append(p.tabScopes, scope)
	p.tabBrowsers = append(p.tabBrowsers, browser)
	return p.tabs, p.tabsErr
}

func browserTestScope() map[string]any {
	return map[string]any{
		"requestId": "request-1", "sessionId": "session-1", "turnId": "turn-1",
		"workspaceKey": "workspace-1", "workspacePath": "/workspace-1",
		"workspaceIdentity": "identity-1", "remoteSessionId": "remote-1",
		"clientMode": "desktop-continuous", "sessionContext": "live",
	}
}

func browserTestExecute() map[string]any {
	params := browserTestScope()
	params["browserId"] = "chrome-1"
	params["browserGeneration"] = 7
	params["command"] = map[string]any{"method": "listUserTabs"}
	return params
}

func browserTestRPC(t *testing.T, c *zcodeClient, buf *bytes.Buffer, method string, params any) map[string]any {
	t.Helper()
	buf.Reset()
	wire, err := json.Marshal(map[string]any{"id": "server-1", "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	c.handleLine(string(wire))
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid response %q: %v", buf.String(), err)
	}
	if out["id"] != "server-1" || len(out) != 2 {
		t.Fatalf("invalid envelope: %#v", out)
	}
	return out
}

func browserTestResult(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result, got %#v", out)
	}
	return result
}

func browserTestError(t *testing.T, out map[string]any, code string) {
	t.Helper()
	result := browserTestResult(t, out)
	err, ok := result["error"].(map[string]any)
	if !ok || result["ok"] != false || err["code"] != code || len(err) != 2 || len(result) != 3 {
		t.Fatalf("expected browser error %s, got %#v", code, result)
	}
	if elapsed, ok := result["elapsedMs"].(float64); !ok || elapsed < 0 {
		t.Fatalf("invalid elapsedMs: %#v", result)
	}
}

func browserTestRPCError(t *testing.T, out map[string]any, code float64) {
	t.Helper()
	err, ok := out["error"].(map[string]any)
	if !ok || err["code"] != code || len(err) != 2 {
		t.Fatalf("expected RPC error %v: %#v", code, out)
	}
}

func TestZcodeBrowserListAndExecute(t *testing.T) {
	p := &fakeZcodeBrowserProvider{
		browsers: []ZcodeBrowser{{ID: "chrome-1", Generation: 7, Name: "Chrome"}},
		tabs:     []ZcodeUserTab{{ID: "tab-1", URL: strPtr("https://example.test/?secret=value"), Title: strPtr("Example"), LastOpened: strPtr("2026-01-01"), TabGroup: strPtr("Work")}},
	}
	var buf bytes.Buffer
	c := &zcodeClient{cfg: Config{ZcodeBrowserProvider: p}, stdin: &buf}
	result := browserTestResult(t, browserTestRPC(t, c, &buf, "interaction/browserList", browserTestScope()))
	if len(result) != 1 {
		t.Fatalf("unexpected list result %#v", result)
	}
	browsers, ok := result["browsers"].([]any)
	if !ok || len(browsers) != 1 {
		t.Fatalf("invalid browser list %#v", result)
	}
	b := browsers[0].(map[string]any)
	if len(b) != 6 || b["id"] != "chrome-1" || b["generation"] != float64(7) || b["type"] != "cdp" || b["name"] != "Chrome" {
		t.Fatalf("invalid descriptor %#v", b)
	}
	capabilities := b["capabilities"].(map[string]any)
	if len(capabilities) != 2 || len(capabilities["browser"].([]any)) != 0 || len(capabilities["tab"].([]any)) != 0 {
		t.Fatalf("advertised capabilities %#v", capabilities)
	}
	overrides := b["apiSupportOverrides"].(map[string]any)
	for _, key := range []string{"Browser.tabs", "Browser.nameSession", "BrowserUser.claimTab", "BrowserUser.history"} {
		if overrides[key] != false {
			t.Fatalf("%s incorrectly advertised: %#v", key, overrides)
		}
	}
	if overrides["BrowserUser.openTabs"] != true {
		t.Fatalf("openTabs must be available: %#v", overrides)
	}

	params := browserTestExecute()
	params["requestId"] = "request-2"
	result = browserTestResult(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", params))
	if result["ok"] != true || len(result) != 3 {
		t.Fatalf("invalid execute result %#v", result)
	}
	if elapsed, ok := result["elapsedMs"].(float64); !ok || elapsed < 0 {
		t.Fatalf("invalid elapsedMs %#v", result)
	}
	tabs := result["userTabs"].([]any)
	if len(tabs) != 1 {
		t.Fatalf("invalid tabs %#v", tabs)
	}
	tab := tabs[0].(map[string]any)
	if len(tab) != 5 || tab["id"] != "tab-1" || tab["tabId"] != nil || tab["url"] != *p.tabs[0].URL || tab["title"] != "Example" || tab["lastOpened"] != "2026-01-01" || tab["tabGroup"] != "Work" {
		t.Fatalf("invalid tab %#v", tab)
	}
	if len(p.listScopes) != 2 || len(p.tabScopes) != 1 || p.tabScopes[0] != p.listScopes[1] || p.tabScopes[0].RequestID != "request-2" || !reflect.DeepEqual(p.tabBrowsers, p.browsers) {
		t.Fatalf("wrong provider routing: %#v %#v %#v", p.listScopes, p.tabScopes, p.tabBrowsers)
	}
}

func strPtr(s string) *string { return &s }

func TestZcodeBrowserEmptyAndErrors(t *testing.T) {
	var buf bytes.Buffer
	p := &fakeZcodeBrowserProvider{}
	c := &zcodeClient{cfg: Config{ZcodeBrowserProvider: p}, stdin: &buf}
	result := browserTestResult(t, browserTestRPC(t, c, &buf, "interaction/browserList", browserTestScope()))
	if !reflect.DeepEqual(result["browsers"], []any{}) {
		t.Fatalf("empty discovery: %#v", result)
	}
	p.browsers = []ZcodeBrowser{{ID: "chrome-1", Generation: 7, Name: "Chrome"}}
	result = browserTestResult(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", browserTestExecute()))
	if result["ok"] != true || !reflect.DeepEqual(result["userTabs"], []any{}) {
		t.Fatalf("empty tabs: %#v", result)
	}

	p.listErr = errors.New("secret discovery diagnostic")
	out := browserTestRPC(t, c, &buf, "interaction/browserList", browserTestScope())
	browserTestRPCError(t, out, -32603)
	if strings.Contains(buf.String(), "secret") {
		t.Fatal("leaked provider error")
	}
	browserTestError(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", browserTestExecute()), "backend_unavailable")
	p.listErr = nil
	p.tabsErr = errors.New("secret tab diagnostic")
	browserTestError(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", browserTestExecute()), "execution_error")
	if strings.Contains(buf.String(), "secret") {
		t.Fatal("leaked provider error")
	}
	p.tabsErr = nil
	p.tabs = []ZcodeUserTab{{ID: ""}}
	browserTestError(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", browserTestExecute()), "execution_error")
	p.browsers = []ZcodeBrowser{{ID: "chrome-1", Generation: -1, Name: "Chrome"}}
	browserTestRPCError(t, browserTestRPC(t, c, &buf, "interaction/browserList", browserTestScope()), -32603)
	browserTestError(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", browserTestExecute()), "backend_unavailable")

	c.cfg.ZcodeBrowserProvider = nil
	result = browserTestResult(t, browserTestRPC(t, c, &buf, "interaction/browserList", browserTestScope()))
	if !reflect.DeepEqual(result["browsers"], []any{}) {
		t.Fatalf("default discovery: %#v", result)
	}
	browserTestError(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", browserTestExecute()), "backend_unavailable")
}

func TestZcodeBrowserExecuteRejectsEveryOtherCommand(t *testing.T) {
	// All methods in the installed ZCode command discriminated union, plus an
	// unknown future command. Rejection must happen before any provider access.
	methods := strings.Fields("navigate back forward reload click fill type press cuaKeypress scroll cuaScroll domCuaScroll hover select check drag cuaDrag screenshot getState elementInfo evaluate getDialog handleDialog waitFor playwright playwrightWaitForTimeout capabilities browserVisibilityGet browserVisibilitySet browserViewportSet browserViewportReset recordingStart recordingStatus recordingCancel activateTab newTab finalize finalizeTabs markDeliverable markHandoff nameSession openScope releaseScope turnEnded closeSession cancelRequest close list futureMethod")
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			p := &fakeZcodeBrowserProvider{browsers: []ZcodeBrowser{{ID: "chrome-1", Generation: 7, Name: "Chrome"}}}
			var buf bytes.Buffer
			c := &zcodeClient{cfg: Config{ZcodeBrowserProvider: p}, stdin: &buf}
			params := browserTestExecute()
			params["command"] = map[string]any{"method": method, "url": "sensitive"}
			browserTestError(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", params), "capability_unsupported")
			if len(p.listScopes) != 0 || len(p.tabScopes) != 0 {
				t.Fatalf("provider called for %s", method)
			}
		})
	}
}

func TestZcodeBrowserScopeAndGeneration(t *testing.T) {
	p := &fakeZcodeBrowserProvider{listFn: func(scope ZcodeBrowserScope) ([]ZcodeBrowser, error) {
		if scope.SessionID == "session-1" && scope.WorkspaceKey == "workspace-1" && scope.WorkspacePath == "/workspace-1" && scope.WorkspaceIdentity == "identity-1" && scope.RemoteSessionID == "remote-1" && scope.ClientMode == "desktop-continuous" && scope.SessionContext == "live" {
			return []ZcodeBrowser{{ID: "chrome-1", Generation: 7, Name: "Chrome"}}, nil
		}
		return nil, nil
	}}
	var buf bytes.Buffer
	c := &zcodeClient{cfg: Config{ZcodeBrowserProvider: p}, stdin: &buf}
	browserTestResult(t, browserTestRPC(t, c, &buf, "interaction/browserList", browserTestScope()))
	for _, mutation := range []struct {
		key   string
		value any
	}{
		{"browserId", "chrome-other"}, {"browserGeneration", 6}, {"sessionId", "other"},
		{"workspaceKey", "other"}, {"workspacePath", "/other"}, {"workspaceIdentity", "other"},
		{"remoteSessionId", "other"}, {"clientMode", "web-remote-replayable"}, {"sessionContext", "cached"},
		{"controlScopeId", "not-supported"},
	} {
		t.Run(mutation.key, func(t *testing.T) {
			params := browserTestExecute()
			params[mutation.key] = mutation.value
			before := len(p.tabScopes)
			browserTestError(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", params), "backend_unavailable")
			if len(p.tabScopes) != before {
				t.Fatal("called tabs for a different scope or generation")
			}
		})
	}
}

func TestZcodeBrowserInvalidParams(t *testing.T) {
	p := &fakeZcodeBrowserProvider{}
	var buf bytes.Buffer
	c := &zcodeClient{cfg: Config{ZcodeBrowserProvider: p}, stdin: &buf}
	for _, tc := range []struct {
		method, key string
		value       any
	}{
		{"interaction/browserList", "requestId", ""}, {"interaction/browserList", "sessionId", nil},
		{"interaction/browserList", "workspaceKey", " "}, {"interaction/browserList", "workspacePath", ""},
		{"interaction/browserList", "clientMode", "unknown"}, {"interaction/browserList", "sessionContext", "unknown"},
		{"interaction/browserList", "extra", true}, {"interaction/browserList", "RequestId", "wrong-case"},
		{"interaction/browserExecute", "browserGeneration", -1}, {"interaction/browserExecute", "browserGeneration", 1.5},
		{"interaction/browserExecute", "browserGeneration", nil}, {"interaction/browserExecute", "browserId", ""},
		{"interaction/browserExecute", "command", map[string]any{"method": "listUserTabs", "tabId": "x"}},
		{"interaction/browserExecute", "command", map[string]any{}}, {"interaction/browserExecute", "extra", true},
	} {
		t.Run(fmt.Sprintf("%s/%s/%v", tc.method, tc.key, tc.value), func(t *testing.T) {
			params := browserTestScope()
			if tc.method == "interaction/browserExecute" {
				params = browserTestExecute()
			}
			params[tc.key] = tc.value
			browserTestRPCError(t, browserTestRPC(t, c, &buf, tc.method, params), -32602)
		})
	}
	if len(p.listScopes) != 0 || len(p.tabScopes) != 0 {
		t.Fatal("invalid params reached provider")
	}
}

func TestZcodeBrowserDoesNotLogSensitiveFrames(t *testing.T) {
	var response, logs bytes.Buffer
	p := &fakeZcodeBrowserProvider{listErr: errors.New("secret provider diagnostic")}
	c := &zcodeClient{cfg: Config{ZcodeBrowserProvider: p, Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}, stdin: &response}
	browserTestRPC(t, c, &response, "interaction/browserList", browserTestScope())
	c.handleLine(`{"method":"interaction/browserExecute","params":{"url":"secret malformed`)
	if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "workspace-1") {
		t.Fatalf("sensitive log: %s", logs.String())
	}
}
