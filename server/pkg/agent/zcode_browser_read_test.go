package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

type browserReadOwner struct {
	SessionID, WorkspaceKey, WorkspacePath, WorkspaceIdentity, RemoteSessionID, ClientMode, SessionContext string
	BrowserID                                                                                              string
	Generation                                                                                             int64
}

func readOwner(scope ZcodeBrowserScope, browser ZcodeBrowser) browserReadOwner {
	return browserReadOwner{scope.SessionID, scope.WorkspaceKey, scope.WorkspacePath, scope.WorkspaceIdentity, scope.RemoteSessionID, scope.ClientMode, scope.SessionContext, browser.ID, browser.Generation}
}

type fakeZcodeBrowserReadProvider struct {
	*fakeZcodeBrowserProvider
	enumerated                map[browserReadOwner]bool
	claimed                   map[browserReadOwner]string
	claimCalls, snapshotCalls int
	claimErr, snapshotErr     error
	tab                       ZcodeBrowserTab
	snapshot                  ZcodeBrowserSnapshot
	lastOptions               ZcodeBrowserSnapshotOptions
}

func newFakeReadProvider() *fakeZcodeBrowserReadProvider {
	dom := []ZcodeBrowserDOMNode{{Tag: "main", Depth: 0, InViewport: true, Text: strPtr("visible page content")}}
	return &fakeZcodeBrowserReadProvider{
		fakeZcodeBrowserProvider: &fakeZcodeBrowserProvider{
			browsers: []ZcodeBrowser{{ID: "chrome-1", Generation: 7, Name: "Chrome"}},
			tabs:     []ZcodeUserTab{{ID: "user-tab-1"}},
		},
		enumerated: make(map[browserReadOwner]bool),
		claimed:    make(map[browserReadOwner]string),
		tab:        ZcodeBrowserTab{TabID: "controlled-tab-1", URL: "https://example.test/", Title: "Example", Viewport: ZcodeBrowserViewport{Width: 1440, Height: 900}, Lifecycle: "active"},
		snapshot:   ZcodeBrowserSnapshot{URL: "https://example.test/", Title: "Example", DOM: &dom, Elements: []ZcodeBrowserSnapshotElement{}, Truncated: false},
	}
}

func (p *fakeZcodeBrowserReadProvider) ListUserTabs(ctx context.Context, scope ZcodeBrowserScope, browser ZcodeBrowser) ([]ZcodeUserTab, error) {
	tabs, err := p.fakeZcodeBrowserProvider.ListUserTabs(ctx, scope, browser)
	if err == nil {
		p.enumerated[readOwner(scope, browser)] = true
	}
	return tabs, err
}

func (p *fakeZcodeBrowserReadProvider) ClaimTab(_ context.Context, scope ZcodeBrowserScope, browser ZcodeBrowser, id string) (ZcodeBrowserTab, error) {
	p.claimCalls++
	if p.claimErr != nil {
		return ZcodeBrowserTab{}, p.claimErr
	}
	owner := readOwner(scope, browser)
	if !p.enumerated[owner] || id != "user-tab-1" {
		return ZcodeBrowserTab{}, errors.New("not listed in scope")
	}
	p.claimed[owner] = p.tab.TabID
	return p.tab, nil
}

func (p *fakeZcodeBrowserReadProvider) Snapshot(_ context.Context, scope ZcodeBrowserScope, browser ZcodeBrowser, id string, options ZcodeBrowserSnapshotOptions) (ZcodeBrowserSnapshot, error) {
	p.snapshotCalls++
	p.lastOptions = options
	if p.snapshotErr != nil {
		return ZcodeBrowserSnapshot{}, p.snapshotErr
	}
	if p.claimed[readOwner(scope, browser)] != id || id == "" {
		return ZcodeBrowserSnapshot{}, errors.New("not owned")
	}
	return p.snapshot, nil
}

func browserReadCommand(method, tabID string) map[string]any {
	params := browserTestExecute()
	params["command"] = map[string]any{"method": method, "tabId": tabID}
	return params
}

func browserReadSetup(t *testing.T) (*fakeZcodeBrowserReadProvider, *zcodeClient, *bytes.Buffer) {
	t.Helper()
	p := newFakeReadProvider()
	buf := &bytes.Buffer{}
	return p, &zcodeClient{cfg: Config{ZcodeBrowserProvider: p}, stdin: buf}, buf
}

func TestZcodeBrowserClaimAndSnapshot(t *testing.T) {
	p, c, buf := browserReadSetup(t)
	list := browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserList", browserTestScope()))
	desc := list["browsers"].([]any)[0].(map[string]any)
	overrides := desc["apiSupportOverrides"].(map[string]any)
	for _, key := range []string{"BrowserUser.claimTab", "BrowserUser.openTabs", "Tab.dom_cua", "DomCUAAPI.get_visible_dom"} {
		if overrides[key] != true {
			t.Fatalf("%s must be enabled: %#v", key, overrides)
		}
	}
	for _, key := range []string{"Browser.tabs", "BrowserUser.history", "Tab.goto", "Tab.close", "Tab.cua", "Tab.playwright", "Tab.recording", "DomCUAAPI.click", "DomCUAAPI.double_click", "DomCUAAPI.downloadMedia", "DomCUAAPI.keypress", "DomCUAAPI.scroll", "DomCUAAPI.type"} {
		if overrides[key] != false {
			t.Fatalf("%s must be disabled: %#v", key, overrides)
		}
	}
	browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("claimTab", "user-tab-1")), "execution_error")
	browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserTestExecute()))
	claim := browserReadCommand("claimTab", "user-tab-1")
	claim["requestId"] = "another-request"
	claim["turnId"] = "another-turn"
	result := browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", claim))
	expectedTab := map[string]any{"tabId": "controlled-tab-1", "url": "https://example.test/", "title": "Example", "viewport": map[string]any{"width": float64(1440), "height": float64(900)}, "lifecycle": "active"}
	if result["ok"] != true || len(result) != 3 || !reflect.DeepEqual(result["tab"], expectedTab) {
		t.Fatalf("bad claim result: %#v", result)
	}
	params := browserReadCommand("snapshot", "controlled-tab-1")
	params["command"].(map[string]any)["includeHidden"] = false
	params["command"].(map[string]any)["maxElements"] = 4
	result = browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", params))
	wantSnapshot := map[string]any{"url": "https://example.test/", "title": "Example", "dom": []any{map[string]any{"tag": "main", "depth": float64(0), "inViewport": true, "text": "visible page content"}}, "elements": []any{}, "truncated": false}
	if result["ok"] != true || len(result) != 3 || !reflect.DeepEqual(result["snapshot"], wantSnapshot) {
		t.Fatalf("bad snapshot result: %#v", result)
	}
	if p.lastOptions.MaxElements == nil || *p.lastOptions.MaxElements != 4 {
		t.Fatalf("options not forwarded: %#v", p.lastOptions)
	}
}

func TestZcodeBrowserSnapshotElementSchema(t *testing.T) {
	p, c, buf := browserReadSetup(t)
	browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserTestExecute()))
	browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("claimTab", "user-tab-1")))
	checked, disabled, truncated := true, false, true
	p.snapshot.DOMTruncated = &truncated
	p.snapshot.Elements = []ZcodeBrowserSnapshotElement{{
		Ref: "node-1", Tag: "input", Role: strPtr("checkbox"), Name: strPtr("Option"), Text: strPtr(""), Value: strPtr("on"),
		Checked: &checked, Disabled: &disabled, Selector: "#option", XPath: "/html/body/input", Rect: ZcodeBrowserRect{X: 1.5, Y: 2, Width: 20, Height: 10},
		InViewport: true, ParentRef: strPtr("parent"), FramePath: strPtr("0>2"), Attributes: map[string]string{"id": "option"},
	}}
	result := browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("snapshot", "controlled-tab-1")))
	snapshot := result["snapshot"].(map[string]any)
	if snapshot["domTruncated"] != true || snapshot["truncated"] != false {
		t.Fatalf("invalid truncation fields: %#v", snapshot)
	}
	want := map[string]any{
		"ref": "node-1", "tag": "input", "role": "checkbox", "name": "Option", "text": "", "value": "on",
		"checked": true, "disabled": false, "selector": "#option", "xpath": "/html/body/input",
		"rect":       map[string]any{"x": 1.5, "y": float64(2), "width": float64(20), "height": float64(10)},
		"inViewport": true, "parentRef": "parent", "framePath": "0>2", "attributes": map[string]any{"id": "option"},
	}
	if got := snapshot["elements"].([]any); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("invalid element schema: %#v", got)
	}
	if p.lastOptions.MaxElements != nil {
		t.Fatal("omitted maxElements was not preserved")
	}
}

func TestZcodeBrowserReadIsolation(t *testing.T) {
	p, c, buf := browserReadSetup(t)
	browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserTestExecute()))
	browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("claimTab", "user-tab-1")))
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"sessionId", "session-other"}, {"workspaceKey", "other"}, {"workspacePath", "/other"},
		{"workspaceIdentity", "other"}, {"remoteSessionId", "other"}, {"clientMode", "web-remote-replayable"}, {"sessionContext", "cached"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			params := browserReadCommand("snapshot", "controlled-tab-1")
			params[tc.key] = tc.value
			browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", params), "execution_error")
			params = browserReadCommand("claimTab", "user-tab-1")
			params[tc.key] = tc.value
			browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", params), "execution_error")
		})
	}
	for _, tc := range []struct {
		key   string
		value any
	}{{"browserId", "other"}, {"browserGeneration", 8}, {"controlScopeId", "scope"}} {
		params := browserReadCommand("snapshot", "controlled-tab-1")
		params[tc.key] = tc.value
		before := p.snapshotCalls
		browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", params), "backend_unavailable")
		if p.snapshotCalls != before {
			t.Fatal("snapshot reached provider for wrong browser/scope")
		}
	}
	delete(p.claimed, readOwner(browserTestScopeValue(), p.browsers[0]))
	browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("snapshot", "controlled-tab-1")), "execution_error")
}

func browserTestScopeValue() ZcodeBrowserScope {
	return ZcodeBrowserScope{RequestID: "request-1", SessionID: "session-1", TurnID: "turn-1", WorkspaceKey: "workspace-1", WorkspacePath: "/workspace-1", WorkspaceIdentity: "identity-1", RemoteSessionID: "remote-1", ClientMode: "desktop-continuous", SessionContext: "live"}
}

func TestZcodeBrowserReadInvalidCommands(t *testing.T) {
	p, c, buf := browserReadSetup(t)
	for _, command := range []any{
		map[string]any{"method": "claimTab"}, map[string]any{"method": "claimTab", "tabId": ""},
		map[string]any{"method": "claimTab", "tabId": "x", "extra": true}, map[string]any{"method": "claimTab", "tabId": nil},
		map[string]any{"method": "snapshot"}, map[string]any{"method": "snapshot", "tabId": " "},
		map[string]any{"method": "snapshot", "tabId": "x", "maxElements": 0}, map[string]any{"method": "snapshot", "tabId": "x", "maxElements": 1.5},
		map[string]any{"method": "snapshot", "tabId": "x", "maxElements": nil}, map[string]any{"method": "snapshot", "tabId": "x", "includeHidden": nil},
		map[string]any{"method": "snapshot", "tabId": "x", "includeHidden": "false"}, map[string]any{"method": "snapshot", "tabId": "x", "extra": 1},
		json.RawMessage(`{"method":"snapshot","tabId":"x","tabId":"y"}`),
	} {
		params := browserTestExecute()
		params["command"] = command
		browserTestRPCError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", params), -32602)
	}
	params := browserReadCommand("snapshot", "x")
	params["command"].(map[string]any)["includeHidden"] = true
	browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", params), "capability_unsupported")
	if len(p.listScopes) != 0 || p.claimCalls != 0 || p.snapshotCalls != 0 {
		t.Fatal("invalid command reached provider")
	}
}

func TestZcodeBrowserReadUnsupportedProvider(t *testing.T) {
	p := &fakeZcodeBrowserProvider{browsers: []ZcodeBrowser{{ID: "chrome-1", Generation: 7, Name: "Chrome"}}}
	var buf bytes.Buffer
	c := &zcodeClient{cfg: Config{ZcodeBrowserProvider: p}, stdin: &buf}
	for _, command := range []map[string]any{browserReadCommand("claimTab", "user-tab-1"), browserReadCommand("snapshot", "controlled-tab-1")} {
		browserTestError(t, browserTestRPC(t, c, &buf, "interaction/browserExecute", command), "capability_unsupported")
	}
	if len(p.listScopes) != 0 || len(p.tabScopes) != 0 {
		t.Fatal("unsupported provider was called")
	}
}

func TestZcodeBrowserReadProviderErrorsAndInvalidResults(t *testing.T) {
	p, c, buf := browserReadSetup(t)
	browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserTestExecute()))
	p.claimErr = errors.New("secret page body")
	browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("claimTab", "user-tab-1")), "execution_error")
	if strings.Contains(buf.String(), "secret") {
		t.Fatal("provider error leaked")
	}
	p.claimErr = nil
	original := p.tab
	for _, tab := range []ZcodeBrowserTab{
		{TabID: "", Viewport: ZcodeBrowserViewport{Width: 1, Height: 1}},
		{TabID: "x", Viewport: ZcodeBrowserViewport{Width: 0, Height: 1}},
		{TabID: "x", Viewport: ZcodeBrowserViewport{Width: 1, Height: -1}},
		{TabID: "x", Viewport: ZcodeBrowserViewport{Width: 1, Height: 1}, Lifecycle: "closed"},
	} {
		p.tab = tab
		browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("claimTab", "user-tab-1")), "execution_error")
	}
	p.tab = original
	browserTestResult(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("claimTab", "user-tab-1")))
	p.snapshotErr = errors.New("secret page body")
	browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("snapshot", "controlled-tab-1")), "execution_error")
	if strings.Contains(buf.String(), "secret") {
		t.Fatal("provider error leaked")
	}
	p.snapshotErr = nil
	originalSnapshot := p.snapshot
	badDOM := []ZcodeBrowserDOMNode{{Tag: "main", Depth: -1}}
	for _, snapshot := range []ZcodeBrowserSnapshot{
		{Elements: []ZcodeBrowserSnapshotElement{}},
		{DOM: originalSnapshot.DOM},
		{DOM: &badDOM, Elements: []ZcodeBrowserSnapshotElement{}},
		{DOM: originalSnapshot.DOM, Elements: []ZcodeBrowserSnapshotElement{{Ref: ""}}},
		{DOM: originalSnapshot.DOM, Elements: []ZcodeBrowserSnapshotElement{{Ref: "ref", Rect: ZcodeBrowserRect{X: math.NaN()}}}},
	} {
		p.snapshot = snapshot
		browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", browserReadCommand("snapshot", "controlled-tab-1")), "execution_error")
	}
	p.snapshot = originalSnapshot
	p.snapshot.Elements = []ZcodeBrowserSnapshotElement{{Ref: "a"}, {Ref: "b"}}
	params := browserReadCommand("snapshot", "controlled-tab-1")
	params["command"].(map[string]any)["maxElements"] = 1
	browserTestError(t, browserTestRPC(t, c, buf, "interaction/browserExecute", params), "execution_error")
}
