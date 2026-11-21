package daemon

import (
	"context"
	"errors"

	"github.com/multica-ai/multica/server/internal/daemon/localenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

type chromeDiscovery interface {
	Discover(context.Context) (string, error)
	List(context.Context) (string, []localenv.ChromeTab, error)
}

type zcodeChromeProvider struct {
	discovery chromeDiscovery
}

func (p zcodeChromeProvider) ListBrowsers(ctx context.Context, _ agent.ZcodeBrowserScope) ([]agent.ZcodeBrowser, error) {
	id, err := p.discovery.Discover(ctx)
	if errors.Is(err, localenv.ErrChromeNotRunning) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []agent.ZcodeBrowser{{ID: id, Name: "System Google Chrome"}}, nil
}

func (p zcodeChromeProvider) ListUserTabs(ctx context.Context, _ agent.ZcodeBrowserScope, browser agent.ZcodeBrowser) ([]agent.ZcodeUserTab, error) {
	id, tabs, err := p.discovery.List(ctx)
	if err != nil {
		switch {
		case errors.Is(err, localenv.ErrChromeAutomationDenied):
			return nil, agent.ErrBrowserAutomationDenied
		case errors.Is(err, localenv.ErrChromeExited):
			return nil, agent.ErrBrowserExited
		default:
			return nil, err
		}
	}
	if id != browser.ID || browser.Generation != 0 {
		return nil, errors.New("Chrome instance changed")
	}
	result := make([]agent.ZcodeUserTab, 0, len(tabs))
	for _, tab := range tabs {
		title, url := tab.Title, tab.URL
		result = append(result, agent.ZcodeUserTab{ID: tab.ID, Title: &title, URL: &url})
	}
	return result, nil
}
