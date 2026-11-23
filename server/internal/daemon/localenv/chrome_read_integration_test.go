//go:build darwin && chromeintegration

package localenv

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestChromeReadRealProduct(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_CHROME_READONLY") != "1" {
		t.Skip("explicit authorization required")
	}
	d := NewChromeDiscovery()
	instance, tabs, err := d.ListJXARefs(context.Background())
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	var matches []ChromeJXATab
	productPath := regexp.MustCompile(`^/product-information/catalog/[0-9]+$`)
	for _, tab := range tabs {
		u, err := url.Parse(tab.URL)
		if err == nil && strings.Contains(strings.ToLower(u.Hostname()), "duoduo") && productPath.MatchString(u.Path) {
			matches = append(matches, tab)
		}
	}
	t.Logf("tab count=%d; product detail match count=%d", len(tabs), len(matches))
	if len(matches) != 1 {
		t.Fatal("target tab missing or ambiguous")
	}
	page, err := d.ReadJXA(context.Background(), instance, matches[0].Ref)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	u, err := url.Parse(page.URL)
	urlMatches := err == nil && strings.Contains(strings.ToLower(u.Hostname()), "duoduo") && productPath.MatchString(u.Path) && page.URL == matches[0].URL
	titlePresent := strings.TrimSpace(page.Title) != ""
	priceIndicator := regexp.MustCompile(`(?i)([$¥￥€£]\s*[0-9]|[0-9]\s*(usd|cny|rmb|元|美元)|价格|售价|price)`).MatchString(page.Text)
	t.Logf("title present=%t; URL target match=%t; price indicator present=%t; truncated=%t", titlePresent, urlMatches, priceIndicator, page.Truncated)
	if !titlePresent || !urlMatches || !priceIndicator {
		t.Fatal("target read did not satisfy the acceptance checks")
	}
}
