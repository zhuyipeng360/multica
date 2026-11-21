package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// ZcodeBrowserProvider supplies read-only Chrome discovery. ListBrowsers must
// return only browsers available to the supplied request scope, with an ID and
// generation that identify the current browser instance. ListUserTabs must not
// claim, activate, or otherwise modify any tab. Implementations must respect ctx
// and may be called for every execute request.
type ZcodeBrowserProvider interface {
	ListBrowsers(ctx context.Context, scope ZcodeBrowserScope) ([]ZcodeBrowser, error)
	ListUserTabs(ctx context.Context, scope ZcodeBrowserScope, browser ZcodeBrowser) ([]ZcodeUserTab, error)
}

// ZcodeBrowserScope is the routing context supplied by the ZCode server. It is
// passed to discovery on every request, rather than using a process-wide cache.
type ZcodeBrowserScope struct {
	RequestID         string `json:"requestId"`
	SessionID         string `json:"sessionId"`
	TurnID            string `json:"turnId,omitempty"`
	WorkspaceKey      string `json:"workspaceKey,omitempty"`
	WorkspacePath     string `json:"workspacePath,omitempty"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
	RemoteSessionID   string `json:"remoteSessionId,omitempty"`
	ClientMode        string `json:"clientMode,omitempty"`
	SessionContext    string `json:"sessionContext,omitempty"`
}

type ZcodeBrowser struct {
	ID         string
	Generation int64
	Name       string
}

// ZcodeUserTab uses the protocol's userTabs.id, not the controlled tabId.
type ZcodeUserTab struct {
	ID         string  `json:"id"`
	LastOpened *string `json:"lastOpened,omitempty"`
	TabGroup   *string `json:"tabGroup,omitempty"`
	Title      *string `json:"title,omitempty"`
	URL        *string `json:"url,omitempty"`
}

type zcodeBrowserExecuteParams struct {
	ZcodeBrowserScope
	ControlScopeID    string          `json:"controlScopeId,omitempty"`
	BrowserID         string          `json:"browserId,omitempty"`
	BrowserGeneration *int64          `json:"browserGeneration,omitempty"`
	Command           json.RawMessage `json:"command"`
}

type zcodeBrowserDescriptor struct {
	ID                  string          `json:"id"`
	Generation          int64           `json:"generation"`
	Type                string          `json:"type"`
	Name                string          `json:"name"`
	Capabilities        map[string]any  `json:"capabilities"`
	APISupportOverrides map[string]bool `json:"apiSupportOverrides"`
}

// Empty capabilities alone are insufficient: the ZCode SDK assumes most API
// members exist by default. Hide control surfaces instead of advertising them.
func zcodeReadOnlyBrowserDescriptor(b ZcodeBrowser) zcodeBrowserDescriptor {
	return zcodeBrowserDescriptor{
		ID: b.ID, Generation: b.Generation, Type: "cdp", Name: b.Name,
		Capabilities: map[string]any{"browser": []any{}, "tab": []any{}},
		APISupportOverrides: map[string]bool{
			"Browser.tabs":               false,
			"Tabs.list":                  false,
			"Tabs.selected":              false,
			"Tabs.get":                   false,
			"Tabs.new":                   false,
			"Tabs.finalize":              false,
			"Browser.nameSession":        false,
			"BrowserUser.claimTab":       false,
			"BrowserUser.history":        false,
			"BrowserUser.openTabs":       true,
			"BrowserRecordingAPI.start":  false,
			"BrowserRecordingAPI.status": false,
			"BrowserRecordingAPI.cancel": false,
		},
	}
}

func zcodeDecodeBrowserParams(raw json.RawMessage, dst any, list bool) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	allowed := map[string]bool{
		"requestId": true, "sessionId": true, "turnId": true,
		"workspaceKey": true, "workspacePath": true, "workspaceIdentity": true,
		"remoteSessionId": true, "clientMode": true, "sessionContext": true,
	}
	if !list {
		allowed["controlScopeId"] = true
		allowed["browserId"] = true
		allowed["browserGeneration"] = true
		allowed["command"] = true
	}
	// Zod's strict objects reject unknown keys and explicit null optionals.
	for key, value := range fields {
		if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return false
	}
	var scope ZcodeBrowserScope
	switch v := dst.(type) {
	case *ZcodeBrowserScope:
		scope = *v
	case *zcodeBrowserExecuteParams:
		scope = v.ZcodeBrowserScope
	default:
		return false
	}
	stringsByField := map[string]string{
		"requestId": scope.RequestID, "sessionId": scope.SessionID,
		"turnId": scope.TurnID, "workspaceKey": scope.WorkspaceKey,
		"workspacePath": scope.WorkspacePath, "workspaceIdentity": scope.WorkspaceIdentity,
		"remoteSessionId": scope.RemoteSessionID,
	}
	if v, ok := dst.(*zcodeBrowserExecuteParams); ok {
		stringsByField["controlScopeId"] = v.ControlScopeID
		stringsByField["browserId"] = v.BrowserID
		if v.BrowserGeneration != nil && *v.BrowserGeneration < 0 {
			return false
		}
	}
	for name, value := range stringsByField {
		_, present := fields[name]
		if (present || name == "requestId" || name == "sessionId" || (list && (name == "workspaceKey" || name == "workspacePath"))) && strings.TrimSpace(value) == "" {
			return false
		}
	}
	if _, ok := fields["clientMode"]; (list || ok) && scope.ClientMode != "desktop-continuous" && scope.ClientMode != "web-remote-replayable" {
		return false
	}
	if _, ok := fields["sessionContext"]; (list || ok) && scope.SessionContext != "live" && scope.SessionContext != "cached" {
		return false
	}
	return true
}

func zcodeValidBrowsers(browsers []ZcodeBrowser) bool {
	seen := make(map[string]bool, len(browsers))
	for _, b := range browsers {
		if b.ID == "" || strings.TrimSpace(b.ID) != b.ID || b.Name == "" || strings.TrimSpace(b.Name) != b.Name || b.Generation < 0 || seen[b.ID] {
			return false
		}
		seen[b.ID] = true
	}
	return true
}

func (c *zcodeClient) browserContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *zcodeClient) handleBrowserList(id, raw json.RawMessage) {
	var scope ZcodeBrowserScope
	if !zcodeDecodeBrowserParams(raw, &scope, true) {
		c.respondError(id, -32602, "invalid browser list parameters")
		return
	}
	result := make([]zcodeBrowserDescriptor, 0)
	if provider := c.cfg.ZcodeBrowserProvider; provider != nil {
		browsers, err := provider.ListBrowsers(c.browserContext(), scope)
		if err != nil || !zcodeValidBrowsers(browsers) {
			c.respondError(id, -32603, "browser discovery failed")
			return
		}
		for _, browser := range browsers {
			result = append(result, zcodeReadOnlyBrowserDescriptor(browser))
		}
	}
	c.respond(id, map[string]any{"browsers": result})
}

type zcodeBrowserError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type zcodeBrowserResult struct {
	OK        bool               `json:"ok"`
	UserTabs  *[]ZcodeUserTab    `json:"userTabs,omitempty"`
	Error     *zcodeBrowserError `json:"error,omitempty"`
	ElapsedMS int64              `json:"elapsedMs"`
}

func (c *zcodeClient) handleBrowserExecute(id, raw json.RawMessage) {
	start := time.Now()
	fail := func(code, message string) {
		c.respond(id, zcodeBrowserResult{OK: false, Error: &zcodeBrowserError{Code: code, Message: message}, ElapsedMS: time.Since(start).Milliseconds()})
	}
	var params zcodeBrowserExecuteParams
	if !zcodeDecodeBrowserParams(raw, &params, false) {
		c.respondError(id, -32602, "invalid browser execute parameters")
		return
	}
	var command map[string]json.RawMessage
	var method string
	if json.Unmarshal(params.Command, &command) != nil || command == nil || json.Unmarshal(command["method"], &method) != nil || method == "" {
		c.respondError(id, -32602, "invalid browser command")
		return
	}
	if method != "listUserTabs" {
		fail("capability_unsupported", "browser command is not supported")
		return
	}
	if len(command) != 1 {
		c.respondError(id, -32602, "invalid browser command")
		return
	}
	provider := c.cfg.ZcodeBrowserProvider
	if provider == nil || params.BrowserID == "" || params.BrowserGeneration == nil || params.ControlScopeID != "" {
		fail("backend_unavailable", "browser backend is unavailable")
		return
	}
	// A descriptor from another workspace, session, or older generation is not
	// authorization. Resolve it again against the current request's scope.
	browsers, err := provider.ListBrowsers(c.browserContext(), params.ZcodeBrowserScope)
	if err != nil || !zcodeValidBrowsers(browsers) {
		fail("backend_unavailable", "browser discovery failed")
		return
	}
	for _, browser := range browsers {
		if browser.ID != params.BrowserID || browser.Generation != *params.BrowserGeneration {
			continue
		}
		tabs, err := provider.ListUserTabs(c.browserContext(), params.ZcodeBrowserScope, browser)
		if err != nil {
			fail("execution_error", "failed to list user tabs")
			return
		}
		for _, tab := range tabs {
			if tab.ID == "" {
				fail("execution_error", "invalid user tab returned by provider")
				return
			}
		}
		if tabs == nil {
			tabs = []ZcodeUserTab{}
		}
		c.respond(id, zcodeBrowserResult{OK: true, UserTabs: &tabs, ElapsedMS: time.Since(start).Milliseconds()})
		return
	}
	fail("backend_unavailable", "browser backend is unavailable")
}
