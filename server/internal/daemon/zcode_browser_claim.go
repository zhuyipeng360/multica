package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/multica-ai/multica/server/internal/daemon/localenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

type chromeClaimDiscovery interface {
	ListWithSource(context.Context) (string, []localenv.ChromeTab, localenv.ChromeListSource, error)
	ClaimJXATab(context.Context, string, string) (localenv.ChromeJXAClaim, error)
	SnapshotJXATab(context.Context, string, string) (localenv.ChromeJXASnapshot, error)
}

type chromeScopeKey struct {
	session, workspaceKey, workspacePath, workspaceIdentity, remoteSession, clientMode, sessionContext string
	browserID                                                                                          string
	generation                                                                                         int64
}

type chromeClaims struct {
	mu      sync.Mutex
	listed  map[chromeScopeKey]map[string]bool
	claimed map[chromeScopeKey]map[string]string
}

func chromeScope(scope agent.ZcodeBrowserScope, browser agent.ZcodeBrowser) (chromeScopeKey, error) {
	if scope.SessionID == "" || scope.WorkspaceKey == "" || scope.WorkspacePath == "" || scope.ClientMode == "" || scope.SessionContext != "live" || browser.ID == "" {
		return chromeScopeKey{}, errors.New("invalid browser scope")
	}
	return chromeScopeKey{scope.SessionID, scope.WorkspaceKey, scope.WorkspacePath, scope.WorkspaceIdentity, scope.RemoteSessionID, scope.ClientMode, scope.SessionContext, browser.ID, browser.Generation}, nil
}

func (p *zcodeChromeProvider) ClaimTab(ctx context.Context, scope agent.ZcodeBrowserScope, browser agent.ZcodeBrowser, userTabID string) (agent.ZcodeBrowserTab, error) {
	key, err := chromeScope(scope, browser)
	if err != nil || p.claims == nil {
		return agent.ZcodeBrowserTab{}, errors.New("invalid browser scope")
	}
	d, ok := p.discovery.(chromeClaimDiscovery)
	if !ok {
		return agent.ZcodeBrowserTab{}, errors.New("claim unsupported")
	}
	p.claims.mu.Lock()
	defer p.claims.mu.Unlock()
	if !p.claims.listed[key][userTabID] {
		return agent.ZcodeBrowserTab{}, errors.New("tab not listed for scope")
	}
	result, err := d.ClaimJXATab(ctx, browser.ID, userTabID)
	if err != nil {
		return agent.ZcodeBrowserTab{}, chromeProviderError(err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ZcodeBrowserTab{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return agent.ZcodeBrowserTab{}, err
	}
	handle := "claimed:" + hex.EncodeToString(random[:])
	if p.claims.claimed[key] == nil {
		p.claims.claimed[key] = map[string]string{}
	}
	p.claims.claimed[key][handle] = userTabID
	active := true
	return agent.ZcodeBrowserTab{TabID: handle, URL: result.URL, Title: result.Title, Viewport: agent.ZcodeBrowserViewport{Width: result.Viewport.Width, Height: result.Viewport.Height}, Active: &active}, nil
}

func (p *zcodeChromeProvider) Snapshot(ctx context.Context, scope agent.ZcodeBrowserScope, browser agent.ZcodeBrowser, tabID string, _ agent.ZcodeBrowserSnapshotOptions) (agent.ZcodeBrowserSnapshot, error) {
	key, err := chromeScope(scope, browser)
	if err != nil || p.claims == nil {
		return agent.ZcodeBrowserSnapshot{}, errors.New("invalid browser scope")
	}
	d, ok := p.discovery.(chromeClaimDiscovery)
	if !ok {
		return agent.ZcodeBrowserSnapshot{}, errors.New("snapshot unsupported")
	}
	p.claims.mu.Lock()
	defer p.claims.mu.Unlock()
	original, ok := p.claims.claimed[key][tabID]
	if !ok {
		return agent.ZcodeBrowserSnapshot{}, errors.New("tab not claimed for scope")
	}
	result, err := d.SnapshotJXATab(ctx, browser.ID, original)
	if err != nil {
		return agent.ZcodeBrowserSnapshot{}, chromeProviderError(err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ZcodeBrowserSnapshot{}, err
	}
	dom := make([]agent.ZcodeBrowserDOMNode, 0, len(result.DOM))
	for _, n := range result.DOM {
		node := agent.ZcodeBrowserDOMNode{Tag: n.Tag, Depth: n.Depth, InViewport: n.InViewport}
		if n.Text != "" {
			text := n.Text
			node.Text = &text
		}
		dom = append(dom, node)
	}
	return agent.ZcodeBrowserSnapshot{URL: result.URL, Title: result.Title, DOM: &dom, DOMTruncated: &result.DOMTruncated, Elements: []agent.ZcodeBrowserSnapshotElement{}, Truncated: result.Truncated}, nil
}

func chromeProviderError(err error) error {
	switch {
	case errors.Is(err, localenv.ErrChromeAutomationDenied):
		return agent.ErrBrowserAutomationDenied
	case errors.Is(err, localenv.ErrChromeExited):
		return agent.ErrBrowserExited
	default:
		return err
	}
}
