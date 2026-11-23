package localenv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrChromeJXAInvalidRef         = errors.New("invalid or stale JXA tab reference")
	ErrChromeJXATabNotFound        = errors.New("JXA tab not found or not unique")
	ErrChromeJXAJavaScriptDisabled = errors.New("Chrome JavaScript execution through Automation is disabled")
	ErrChromeJXAPageChanged        = errors.New("Chrome page changed during read")
)

// ChromeJXATab is an Automation tab reference, not a CDP target ID. Title and
// URL can contain private data and must not be logged.
type ChromeJXATab struct {
	Ref   string
	Title string
	URL   string
}

// ChromeJXAPage contains potentially sensitive page data and must not be logged.
// Text is body.innerText, not a privacy filter; it can contain credentials.
type ChromeJXAPage struct {
	Title     string
	URL       string
	Text      string
	Truncated bool
}

type chromeJXARef struct{ instance, tab string }
type chromeJXARefState struct {
	mu   sync.Mutex
	refs map[string]chromeJXARef
}

type chromeJXAResult struct {
	Status string `json:"status"`
	Tabs   []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"tabs"`
	Title     *string `json:"title"`
	URL       *string `json:"url"`
	Text      *string `json:"text"`
	Truncated *bool   `json:"truncated"`
}

func chromeJXAResultDecode(data []byte) (chromeJXAResult, error) {
	var result chromeJXAResult
	dec := json.NewDecoder(bytes.NewReader(data))
	if dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF {
		return result, ErrChromeJXAUnavailable
	}
	switch result.Status {
	case "ok":
		return result, nil
	case "denied":
		return result, ErrChromeAutomationDenied
	case "exited":
		return result, ErrChromeExited
	case "not_found":
		return result, ErrChromeJXATabNotFound
	case "js_disabled":
		return result, ErrChromeJXAJavaScriptDisabled
	case "changed":
		return result, ErrChromeJXAPageChanged
	default:
		return result, ErrChromeJXAUnavailable
	}
}

func validChromeJXATabID(id string) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && strconv.FormatInt(n, 10) == id
}

func (d *ChromeDiscovery) jxaCall(ctx context.Context, instance, op, tab string) (string, chromeJXAResult, error) {
	if !chromeSupported {
		return "", chromeJXAResult{}, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, proc, err := d.findChromeProcess(ctx)
	if err != nil {
		return "", chromeJXAResult{}, err
	}
	if instance != "" && instance != session.id {
		return "", chromeJXAResult{}, ErrChromeJXAInvalidRef
	}
	if d.deps.jxaRead == nil || !session.verified() || !d.deps.identity(ctx, proc) {
		return "", chromeJXAResult{}, chromeContextError(ctx, ErrChromeIdentityUnverified)
	}
	data, callErr := d.deps.jxaRead(ctx, session.pid, op, tab)
	// Identity takes precedence over adapter errors, and no result survives a change.
	if !session.verified() || !d.deps.identity(ctx, proc) {
		return "", chromeJXAResult{}, chromeContextError(ctx, ErrChromeIdentityUnverified)
	}
	if ctx.Err() != nil {
		return "", chromeJXAResult{}, ctx.Err()
	}
	if callErr != nil {
		switch {
		case errors.Is(callErr, ErrChromeAutomationDenied):
			return "", chromeJXAResult{}, ErrChromeAutomationDenied
		case errors.Is(callErr, ErrChromeExited):
			return "", chromeJXAResult{}, ErrChromeExited
		default:
			return "", chromeJXAResult{}, ErrChromeJXAUnavailable
		}
	}
	result, err := chromeJXAResultDecode(data)
	return session.id, result, err
}

// ListJXARefs enumerates Automation tabs independently of CDP. References are
// random, scoped to this discovery object and instance, and replaced per list.
// Calling this method may prompt for macOS Automation permission.
func (d *ChromeDiscovery) ListJXARefs(ctx context.Context) (string, []ChromeJXATab, error) {
	id, result, err := d.jxaCall(ctx, "", "list", "")
	if err != nil {
		return "", nil, err
	}
	if result.Tabs == nil || len(result.Tabs) > 4096 {
		return "", nil, ErrChromeJXAUnavailable
	}
	refs := make(map[string]chromeJXARef, len(result.Tabs))
	seen := make(map[string]bool, len(result.Tabs))
	tabs := make([]ChromeJXATab, 0, len(result.Tabs))
	for _, tab := range result.Tabs {
		if !validChromeJXATabID(tab.ID) || seen[tab.ID] {
			return "", nil, ErrChromeJXAUnavailable
		}
		seen[tab.ID] = true
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, ErrChromeJXAUnavailable
		}
		ref := "jxa:" + hex.EncodeToString(b[:])
		if _, exists := refs[ref]; exists {
			return "", nil, ErrChromeJXAUnavailable
		}
		refs[ref] = chromeJXARef{instance: id, tab: tab.ID}
		tabs = append(tabs, ChromeJXATab{Ref: ref, Title: tab.Title, URL: safeChromeURL(tab.URL)})
	}
	d.jxaRefs.mu.Lock()
	d.jxaRefs.refs = refs
	d.jxaRefs.mu.Unlock()
	return id, tabs, nil
}

// ReadJXA reads only a reference issued by ListJXARefs on this object for the
// same browser instance. Neither argument is interpreted as executable code.
func (d *ChromeDiscovery) ReadJXA(ctx context.Context, instanceID, ref string) (ChromeJXAPage, error) {
	if !chromeSupported {
		return ChromeJXAPage{}, ErrUnsupported
	}
	d.jxaRefs.mu.Lock()
	entry, ok := d.jxaRefs.refs[ref]
	d.jxaRefs.mu.Unlock()
	if !ok || instanceID == "" || entry.instance != instanceID || !strings.HasPrefix(ref, "jxa:") {
		return ChromeJXAPage{}, ErrChromeJXAInvalidRef
	}
	_, result, err := d.jxaCall(ctx, instanceID, "read", entry.tab)
	if err != nil {
		return ChromeJXAPage{}, err
	}
	if result.Title == nil || result.URL == nil || result.Text == nil || result.Truncated == nil ||
		!utf8.ValidString(*result.Text) || utf8.RuneCountInString(*result.Text) > 20000 ||
		len(*result.Title) > 16384 || len(*result.URL) > 32768 {
		return ChromeJXAPage{}, ErrChromeJXAUnavailable
	}
	cleanURL := safeChromeURL(*result.URL)
	if cleanURL == "" {
		return ChromeJXAPage{}, ErrChromeJXAUnavailable
	}
	return ChromeJXAPage{Title: *result.Title, URL: cleanURL, Text: *result.Text, Truncated: *result.Truncated}, nil
}
