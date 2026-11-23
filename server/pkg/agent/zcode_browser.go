package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
)

var (
	ErrBrowserAutomationDenied = errors.New("macOS Automation permission for Google Chrome was denied")
	ErrBrowserExited           = errors.New("Google Chrome exited while listing tabs")
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

// ZcodeBrowserReadProvider is an optional extension for claiming a previously
// enumerated user tab and reading its visible semantic DOM. ClaimTab must
// verify that userTabID was enumerated for the same stable session/workspace
// scope and browser generation. Snapshot must verify ownership, browser
// generation and the tab's current lifecycle on every call. RequestID and
// TurnID are per-request identifiers and must not define ownership. ClaimTab
// may activate the target once; Snapshot must not activate it. Providers must
// not synthesize viewport, DOM, or element data.
type ZcodeBrowserReadProvider interface {
	ClaimTab(ctx context.Context, scope ZcodeBrowserScope, browser ZcodeBrowser, userTabID string) (ZcodeBrowserTab, error)
	Snapshot(ctx context.Context, scope ZcodeBrowserScope, browser ZcodeBrowser, tabID string, options ZcodeBrowserSnapshotOptions) (ZcodeBrowserSnapshot, error)
}

type ZcodeBrowserViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type ZcodeBrowserTab struct {
	TabID     string               `json:"tabId"`
	URL       string               `json:"url"`
	Title     string               `json:"title"`
	Viewport  ZcodeBrowserViewport `json:"viewport"`
	Active    *bool                `json:"active,omitempty"`
	Lifecycle string               `json:"lifecycle,omitempty"`
}

type ZcodeBrowserSnapshotOptions struct {
	MaxElements *int
}

type ZcodeBrowserRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type ZcodeBrowserSnapshotElement struct {
	Ref        string            `json:"ref"`
	Tag        string            `json:"tag"`
	Role       *string           `json:"role,omitempty"`
	Name       *string           `json:"name,omitempty"`
	Text       *string           `json:"text,omitempty"`
	Value      *string           `json:"value,omitempty"`
	Disabled   *bool             `json:"disabled,omitempty"`
	Checked    *bool             `json:"checked,omitempty"`
	Selector   string            `json:"selector"`
	XPath      string            `json:"xpath"`
	Rect       ZcodeBrowserRect  `json:"rect"`
	InViewport bool              `json:"inViewport"`
	ParentRef  *string           `json:"parentRef,omitempty"`
	FramePath  *string           `json:"framePath,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type ZcodeBrowserDOMNode struct {
	Tag        string            `json:"tag"`
	Depth      int               `json:"depth"`
	InViewport bool              `json:"inViewport"`
	Ref        *string           `json:"ref,omitempty"`
	Role       *string           `json:"role,omitempty"`
	Name       *string           `json:"name,omitempty"`
	Text       *string           `json:"text,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type ZcodeBrowserSnapshot struct {
	URL          string                        `json:"url"`
	Title        string                        `json:"title"`
	DOM          *[]ZcodeBrowserDOMNode        `json:"dom,omitempty"`
	DOMTruncated *bool                         `json:"domTruncated,omitempty"`
	Elements     []ZcodeBrowserSnapshotElement `json:"elements"`
	Truncated    bool                          `json:"truncated"`
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
func zcodeReadOnlyBrowserDescriptor(b ZcodeBrowser, canRead bool) zcodeBrowserDescriptor {
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
			"BrowserUser.claimTab":       canRead,
			"BrowserUser.history":        false,
			"BrowserUser.openTabs":       true,
			"Tab.goto":                   false,
			"Tab.back":                   false,
			"Tab.forward":                false,
			"Tab.reload":                 false,
			"Tab.close":                  false,
			"Tab.url":                    false,
			"Tab.title":                  false,
			"Tab.screenshot":             false,
			"Tab.getJsDialog":            false,
			"Tab.setViewportSize":        false,
			"Tab.recording":              false,
			"Tab.finalize":               false,
			"Tab.markDeliverable":        false,
			"Tab.markHandoff":            false,
			"Tab.cua":                    false,
			"Tab.dom_cua":                canRead,
			"Tab.playwright":             false,
			"DomCUAAPI.get_visible_dom":  canRead,
			"DomCUAAPI.click":            false,
			"DomCUAAPI.double_click":     false,
			"DomCUAAPI.downloadMedia":    false,
			"DomCUAAPI.keypress":         false,
			"DomCUAAPI.scroll":           false,
			"DomCUAAPI.type":             false,
			"BrowserRecordingAPI.start":  false,
			"BrowserRecordingAPI.status": false,
			"BrowserRecordingAPI.cancel": false,
		},
	}
}

func zcodeStrictObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, exists := fields[key]; exists {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, false
	}
	return fields, true
}

func zcodeDecodeBrowserParams(raw json.RawMessage, dst any, list bool) bool {
	fields, ok := zcodeStrictObject(raw)
	if !ok {
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
		_, canRead := provider.(ZcodeBrowserReadProvider)
		for _, browser := range browsers {
			result = append(result, zcodeReadOnlyBrowserDescriptor(browser, canRead))
		}
	}
	c.respond(id, map[string]any{"browsers": result})
}

type zcodeBrowserError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type zcodeBrowserResult struct {
	OK        bool                  `json:"ok"`
	UserTabs  *[]ZcodeUserTab       `json:"userTabs,omitempty"`
	Tab       *ZcodeBrowserTab      `json:"tab,omitempty"`
	Snapshot  *ZcodeBrowserSnapshot `json:"snapshot,omitempty"`
	Error     *zcodeBrowserError    `json:"error,omitempty"`
	ElapsedMS int64                 `json:"elapsedMs"`
}

type zcodeReadCommand struct {
	Method        string `json:"method"`
	TabID         string `json:"tabId"`
	MaxElements   *int   `json:"maxElements"`
	IncludeHidden *bool  `json:"includeHidden"`
}

func zcodeDecodeReadCommand(fields map[string]json.RawMessage, method string) (zcodeReadCommand, bool) {
	var command zcodeReadCommand
	allowed := map[string]bool{"method": true}
	switch method {
	case "claimTab":
		allowed["tabId"] = true
	case "snapshot":
		allowed["tabId"] = true
		allowed["maxElements"] = true
		allowed["includeHidden"] = true
	default:
		return command, false
	}
	for key, value := range fields {
		if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return command, false
		}
	}
	data, err := json.Marshal(fields)
	if err != nil || json.Unmarshal(data, &command) != nil || strings.TrimSpace(command.TabID) == "" || command.TabID != strings.TrimSpace(command.TabID) {
		return command, false
	}
	if command.MaxElements != nil && *command.MaxElements <= 0 {
		return command, false
	}
	return command, true
}

func zcodeValidClaimedTab(tab ZcodeBrowserTab) bool {
	return tab.TabID != "" && strings.TrimSpace(tab.TabID) == tab.TabID &&
		tab.Viewport.Width > 0 && tab.Viewport.Height > 0 &&
		(tab.Lifecycle == "" || tab.Lifecycle == "active" || tab.Lifecycle == "deliverable" || tab.Lifecycle == "handoff")
}

func zcodeValidSnapshot(snapshot ZcodeBrowserSnapshot, maxElements *int) bool {
	if snapshot.DOM == nil || *snapshot.DOM == nil || snapshot.Elements == nil || (maxElements != nil && len(snapshot.Elements) > *maxElements) {
		return false
	}
	for _, node := range *snapshot.DOM {
		if node.Depth < 0 || (node.Ref != nil && *node.Ref == "") {
			return false
		}
	}
	for _, element := range snapshot.Elements {
		if element.Ref == "" || math.IsNaN(element.Rect.X) || math.IsInf(element.Rect.X, 0) ||
			math.IsNaN(element.Rect.Y) || math.IsInf(element.Rect.Y, 0) ||
			math.IsNaN(element.Rect.Width) || math.IsInf(element.Rect.Width, 0) ||
			math.IsNaN(element.Rect.Height) || math.IsInf(element.Rect.Height, 0) {
			return false
		}
	}
	return true
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
	command, ok := zcodeStrictObject(params.Command)
	var method string
	if !ok || json.Unmarshal(command["method"], &method) != nil || method == "" {
		c.respondError(id, -32602, "invalid browser command")
		return
	}
	var readCommand zcodeReadCommand
	switch method {
	case "listUserTabs":
		if len(command) != 1 {
			c.respondError(id, -32602, "invalid browser command")
			return
		}
	case "claimTab", "snapshot":
		var valid bool
		readCommand, valid = zcodeDecodeReadCommand(command, method)
		if !valid {
			c.respondError(id, -32602, "invalid browser command")
			return
		}
		if readCommand.IncludeHidden != nil && *readCommand.IncludeHidden {
			fail("capability_unsupported", "hidden DOM is not supported")
			return
		}
	default:
		fail("capability_unsupported", "browser command is not supported")
		return
	}
	provider := c.cfg.ZcodeBrowserProvider
	if provider == nil || params.BrowserID == "" || params.BrowserGeneration == nil || params.ControlScopeID != "" {
		fail("backend_unavailable", "browser backend is unavailable")
		return
	}
	readProvider, canRead := provider.(ZcodeBrowserReadProvider)
	if method != "listUserTabs" && !canRead {
		fail("capability_unsupported", "browser command is not supported")
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
		if method == "claimTab" {
			tab, err := readProvider.ClaimTab(c.browserContext(), params.ZcodeBrowserScope, browser, readCommand.TabID)
			if err != nil || !zcodeValidClaimedTab(tab) {
				message := "failed to claim user tab"
				if errors.Is(err, ErrBrowserAutomationDenied) {
					message = ErrBrowserAutomationDenied.Error()
				}
				if errors.Is(err, ErrBrowserExited) {
					message = ErrBrowserExited.Error()
				}
				fail("execution_error", message)
				return
			}
			c.respond(id, zcodeBrowserResult{OK: true, Tab: &tab, ElapsedMS: time.Since(start).Milliseconds()})
			return
		}
		if method == "snapshot" {
			snapshot, err := readProvider.Snapshot(c.browserContext(), params.ZcodeBrowserScope, browser, readCommand.TabID, ZcodeBrowserSnapshotOptions{MaxElements: readCommand.MaxElements})
			if err != nil || !zcodeValidSnapshot(snapshot, readCommand.MaxElements) {
				message := "failed to snapshot tab"
				if errors.Is(err, ErrBrowserAutomationDenied) {
					message = ErrBrowserAutomationDenied.Error()
				}
				if errors.Is(err, ErrBrowserExited) {
					message = ErrBrowserExited.Error()
				}
				fail("execution_error", message)
				return
			}
			c.respond(id, zcodeBrowserResult{OK: true, Snapshot: &snapshot, ElapsedMS: time.Since(start).Milliseconds()})
			return
		}
		tabs, err := provider.ListUserTabs(c.browserContext(), params.ZcodeBrowserScope, browser)
		if err != nil {
			message := "failed to list user tabs"
			switch {
			case errors.Is(err, ErrBrowserAutomationDenied):
				message = ErrBrowserAutomationDenied.Error()
			case errors.Is(err, ErrBrowserExited):
				message = ErrBrowserExited.Error()
			}
			fail("execution_error", message)
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
