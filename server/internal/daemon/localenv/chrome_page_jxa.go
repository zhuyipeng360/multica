package localenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"
	"unicode/utf8"
)

var (
	ErrChromeJXAInvalidTab          = errors.New("invalid JXA tab or instance reference")
	ErrChromeJXATabNotFound         = errors.New("JXA tab not found or not unique")
	ErrChromeJXAJavaScriptDisabled  = errors.New("Chrome JavaScript execution through Automation is disabled")
	ErrChromeJXAPageChanged         = errors.New("Chrome page changed during read")
	ErrChromeJXAViewportUnavailable = errors.New("Chrome viewport unavailable")
)

// ChromeViewport is the actual page viewport in CSS pixels.
type ChromeViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ChromeJXAClaim describes a claimed tab. Title and URL may be sensitive.
type ChromeJXAClaim struct {
	TabID    string         `json:"tabId"`
	URL      string         `json:"url"`
	Title    string         `json:"title"`
	Viewport ChromeViewport `json:"viewport"`
}

// ChromeDOMNode is a visible semantic DOM fact, not an accessibility tree.
// Text is potentially sensitive and must not be logged.
type ChromeDOMNode struct {
	Tag        string `json:"tag"`
	Depth      int    `json:"depth"`
	InViewport bool   `json:"inViewport"`
	Text       string `json:"text,omitempty"`
}

// ChromeJXASnapshot contains potentially sensitive visible text, not form
// values. Elements is intentionally empty: this adapter does not fabricate
// selectors, action references, or element geometry.
type ChromeJXASnapshot struct {
	URL          string          `json:"url"`
	Title        string          `json:"title"`
	DOM          []ChromeDOMNode `json:"dom"`
	DOMTruncated bool            `json:"domTruncated"`
	Elements     []struct{}      `json:"elements"`
	Truncated    bool            `json:"truncated"`
}

const (
	chromeJXAMaxOutput = 1 << 20
	chromeJXAMaxNodes  = 500
	chromeJXAMaxText   = 20000
)

type chromeJXARawNode struct {
	Tag        *string `json:"tag"`
	Depth      *int    `json:"depth"`
	InViewport *bool   `json:"inViewport"`
	Text       *string `json:"text"`
}

type chromeJXAPageResult struct {
	Status       string             `json:"status"`
	URL          *string            `json:"url"`
	Title        *string            `json:"title"`
	Viewport     *ChromeViewport    `json:"viewport"`
	DOM          []chromeJXARawNode `json:"dom"`
	DOMTruncated *bool              `json:"domTruncated"`
}

func validChromeJXATabID(id string) bool {
	if len(id) == 0 || len(id) > 20 {
		return false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func decodeChromeJXAPage(data []byte) (chromeJXAPageResult, error) {
	var r chromeJXAPageResult
	if len(data) > chromeJXAMaxOutput || !utf8.Valid(data) {
		return r, ErrChromeJXAUnavailable
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || dec.Decode(new(any)) != io.EOF {
		return r, ErrChromeJXAUnavailable
	}
	switch r.Status {
	case "ok":
		return r, nil
	case "denied":
		return r, ErrChromeAutomationDenied
	case "exited":
		return r, ErrChromeExited
	case "not_found":
		return r, ErrChromeJXATabNotFound
	case "js_disabled":
		return r, ErrChromeJXAJavaScriptDisabled
	case "changed":
		return r, ErrChromeJXAPageChanged
	case "no_viewport":
		return r, ErrChromeJXAViewportUnavailable
	default:
		return r, ErrChromeJXAUnavailable
	}
}

// jxaPageCall never accepts page code from callers. Scope authorization and
// source provenance are the caller's responsibility, not this adapter's state.
func (d *ChromeDiscovery) jxaPageCall(ctx context.Context, instanceID, tabID, op string) (chromeJXAPageResult, error) {
	if !chromeSupported {
		return chromeJXAPageResult{}, ErrUnsupported
	}
	if instanceID == "" || !validChromeJXATabID(tabID) || (op != "claim" && op != "snapshot") {
		return chromeJXAPageResult{}, ErrChromeJXAInvalidTab
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, proc, _, err := d.findChromeProcess(ctx)
	if err != nil {
		return chromeJXAPageResult{}, err
	}
	if instanceID != session.id {
		return chromeJXAPageResult{}, ErrChromeJXAInvalidTab
	}
	if d.deps.jxaPage == nil || !session.verified() || !d.deps.identity(ctx, proc) || !session.verified() {
		return chromeJXAPageResult{}, chromeContextError(ctx, ErrChromeIdentityUnverified)
	}
	data, callErr := d.deps.jxaPage(ctx, session.pid, op, tabID)
	// No result or adapter error survives a process or signature change.
	if !session.verified() || !d.deps.identity(ctx, proc) || !session.verified() {
		return chromeJXAPageResult{}, chromeContextError(ctx, ErrChromeIdentityUnverified)
	}
	if err := ctx.Err(); err != nil {
		return chromeJXAPageResult{}, err
	}
	if callErr != nil {
		switch {
		case errors.Is(callErr, ErrChromeAutomationDenied):
			return chromeJXAPageResult{}, ErrChromeAutomationDenied
		case errors.Is(callErr, ErrChromeExited):
			return chromeJXAPageResult{}, ErrChromeExited
		default:
			return chromeJXAPageResult{}, ErrChromeJXAUnavailable
		}
	}
	return decodeChromeJXAPage(data)
}

func chromeJXAPageMetadata(r chromeJXAPageResult) (string, string, error) {
	if r.URL == nil || r.Title == nil || len(*r.URL) > 32768 || len(*r.Title) > 16384 || !utf8.ValidString(*r.URL) || !utf8.ValidString(*r.Title) {
		return "", "", ErrChromeJXAUnavailable
	}
	url := safeChromeURL(*r.URL)
	if url == "" {
		return "", "", ErrChromeJXAUnavailable
	}
	return url, *r.Title, nil
}

// ClaimJXATab activates a tab once and reads its real viewport. tabID must be
// from a JXA-sourced ListWithSource response for this instance and authorized
// in the caller's scope. CDP IDs, even numeric ones, are not JXA references.
func (d *ChromeDiscovery) ClaimJXATab(ctx context.Context, instanceID, tabID string) (ChromeJXAClaim, error) {
	r, err := d.jxaPageCall(ctx, instanceID, tabID, "claim")
	if err != nil {
		return ChromeJXAClaim{}, err
	}
	url, title, err := chromeJXAPageMetadata(r)
	if err != nil {
		return ChromeJXAClaim{}, err
	}
	if r.Viewport == nil || r.Viewport.Width <= 0 || r.Viewport.Height <= 0 || r.Viewport.Width > 100000 || r.Viewport.Height > 100000 {
		return ChromeJXAClaim{}, ErrChromeJXAViewportUnavailable
	}
	return ChromeJXAClaim{TabID: tabID, URL: url, Title: title, Viewport: *r.Viewport}, nil
}

// SnapshotJXATab reads the uniquely matched JXA tab without activating it.
// The caller must enforce both JXA-list provenance and prior claim in scope.
func (d *ChromeDiscovery) SnapshotJXATab(ctx context.Context, instanceID, tabID string) (ChromeJXASnapshot, error) {
	r, err := d.jxaPageCall(ctx, instanceID, tabID, "snapshot")
	if err != nil {
		return ChromeJXASnapshot{}, err
	}
	url, title, err := chromeJXAPageMetadata(r)
	if err != nil {
		return ChromeJXASnapshot{}, err
	}
	if r.DOM == nil || r.DOMTruncated == nil || len(r.DOM) > chromeJXAMaxNodes {
		return ChromeJXASnapshot{}, ErrChromeJXAUnavailable
	}
	nodes := make([]ChromeDOMNode, 0, len(r.DOM))
	total := 0
	for _, n := range r.DOM {
		if n.Tag == nil || len(*n.Tag) == 0 || len(*n.Tag) > 64 || !utf8.ValidString(*n.Tag) || n.Depth == nil || *n.Depth < 0 || *n.Depth > 64 || n.InViewport == nil {
			return ChromeJXASnapshot{}, ErrChromeJXAUnavailable
		}
		node := ChromeDOMNode{Tag: *n.Tag, Depth: *n.Depth, InViewport: *n.InViewport}
		if n.Text != nil {
			if !utf8.ValidString(*n.Text) || utf8.RuneCountInString(*n.Text) > 256 {
				return ChromeJXASnapshot{}, ErrChromeJXAUnavailable
			}
			total += utf8.RuneCountInString(*n.Text)
			node.Text = *n.Text
		}
		nodes = append(nodes, node)
	}
	if total > chromeJXAMaxText {
		return ChromeJXASnapshot{}, ErrChromeJXAUnavailable
	}
	return ChromeJXASnapshot{URL: url, Title: title, DOM: nodes, DOMTruncated: *r.DOMTruncated, Elements: []struct{}{}, Truncated: false}, nil
}
