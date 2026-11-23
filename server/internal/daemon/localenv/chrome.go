package localenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrChromeNotRunning means the default Chrome profile has no lock.
var ErrChromeNotRunning = errors.New("default Chrome is not running")

// ErrChromeIdentityUnverified means a lock exists but its owner cannot be verified.
var ErrChromeIdentityUnverified = errors.New("default Chrome process identity could not be verified")

// ErrChromeCDPUnavailable means the verified Chrome has no usable local DevTools endpoint.
var ErrChromeCDPUnavailable = errors.New("default Chrome DevTools is unavailable")

// ErrChromeAutomationDenied means macOS denied Automation access to Chrome.
var ErrChromeAutomationDenied = errors.New("macOS Automation permission for Google Chrome was denied")

// ErrChromeExited means Chrome exited while its tabs were being read.
var ErrChromeExited = errors.New("default Chrome exited while listing tabs")

// ErrChromeJXAUnavailable means JXA could not read the verified Chrome instance.
var ErrChromeJXAUnavailable = errors.New("unable to list default Chrome tabs using macOS Automation")

// ChromeTab is the deliberately limited, read-only representation of a page target.
// Title and URL may contain private information and should not be logged.
type ChromeTab struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// ChromeDiscovery discovers only the process owning the default Chrome profile.
// It never launches Chrome or turns on debugging. It is safe for concurrent use.
type ChromeDiscovery struct {
	deps chromeDependencies
}

type chromeProcess struct {
	pid   int
	ppid  int
	start uint64
	uid   int
	exe   string
	args  []string
}

type chromeDependencies struct {
	home     func() (string, error)
	hostname func() (string, error)
	uid      func() int
	readlink func(string) (string, error)
	process  func(context.Context, int) (chromeProcess, error)
	identity func(context.Context, chromeProcess) bool
	listener func(context.Context, int, int) bool
	active   func(string) (string, error)
	jxa      func(context.Context, int) ([]ChromeTab, error)
	jxaPage  func(context.Context, int, string, string) ([]byte, error)
	client   *http.Client
}

// NewChromeDiscovery constructs the standalone, read-only Chrome discovery component.
func NewChromeDiscovery() *ChromeDiscovery {
	return &ChromeDiscovery{deps: chromeDependencies{
		home: os.UserHomeDir, hostname: os.Hostname, uid: os.Getuid,
		readlink: os.Readlink, process: chromeProcessInfo, identity: verifyChromeSignature, listener: chromeOwnsListener,
		active: readChromeActivePort, jxa: listChromeTabsJXA, jxaPage: runChromePageJXA, client: localChromeHTTPClient(),
	}}
}

// ListTabs returns tabs from the default Chrome profile using an existing CDP
// listener when available, or macOS Automation otherwise. It never opens targets
// or inspects page contents. Errors distinguish missing Chrome, unverified
// identity, unavailable CDP, and Automation failures.
func (d *ChromeDiscovery) ListTabs(ctx context.Context) ([]ChromeTab, error) {
	_, tabs, err := d.List(ctx)
	return tabs, err
}

// Discover verifies the default Chrome and its endpoint without reading tabs.
// The opaque ID changes when the owning process restarts.
func (d *ChromeDiscovery) Discover(ctx context.Context) (string, error) {
	if !chromeSupported {
		return "", ErrUnsupported
	}
	return d.discover(ctx)
}

func (d *ChromeDiscovery) discover(ctx context.Context) (string, error) {
	session, err := d.findChrome(ctx)
	if err != nil {
		return "", err
	}
	return session.id, nil
}

// List returns the owning instance ID and its page tabs from one verified
// session. A caller with an earlier ID must compare it before using the tabs.
func (d *ChromeDiscovery) List(ctx context.Context) (string, []ChromeTab, error) {
	if !chromeSupported {
		return "", nil, ErrUnsupported
	}
	return d.list(ctx)
}

// ChromeListSource identifies the namespace of IDs returned by a list call.
// CDP target IDs must never be interpreted as JXA tab IDs.
type ChromeListSource string

const (
	ChromeListSourceJXA ChromeListSource = "jxa"
	ChromeListSourceCDP ChromeListSource = "cdp"
)

// ListWithSource preserves List's results and identifies the source of that
// exact response. Callers must bind the source and IDs to their own scope.
func (d *ChromeDiscovery) ListWithSource(ctx context.Context) (string, []ChromeTab, ChromeListSource, error) {
	if !chromeSupported {
		return "", nil, "", ErrUnsupported
	}
	return d.listWithSource(ctx)
}

func (d *ChromeDiscovery) listTabs(ctx context.Context) ([]ChromeTab, error) {
	_, tabs, err := d.list(ctx)
	return tabs, err
}

type chromeSession struct {
	id       string
	pid      int
	port     int
	process  chromeProcess
	verified func() bool
}

func (d *ChromeDiscovery) findChromeProcess(ctx context.Context) (chromeSession, chromeProcess, string, error) {
	if err := ctx.Err(); err != nil {
		return chromeSession{}, chromeProcess{}, "", err
	}
	deps := d.deps
	home, err := deps.home()
	if err != nil || home == "" {
		return chromeSession{}, chromeProcess{}, "", ErrChromeIdentityUnverified
	}
	dir := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	lock := filepath.Join(dir, "SingletonLock")
	host, err := deps.hostname()
	if err != nil || host == "" {
		return chromeSession{}, chromeProcess{}, "", ErrChromeIdentityUnverified
	}
	link, err := deps.readlink(lock)
	if errors.Is(err, os.ErrNotExist) {
		return chromeSession{}, chromeProcess{}, "", ErrChromeNotRunning
	}
	if err != nil {
		return chromeSession{}, chromeProcess{}, "", ErrChromeIdentityUnverified
	}
	pid, ok := chromeLockPID(link, host)
	if !ok {
		return chromeSession{}, chromeProcess{}, "", ErrChromeIdentityUnverified
	}
	proc, err := deps.process(ctx, pid)
	if err != nil || !isDefaultChrome(proc, pid, deps.uid()) || !deps.identity(ctx, proc) {
		return chromeSession{}, chromeProcess{}, "", chromeContextError(ctx, ErrChromeIdentityUnverified)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("chrome:%d:%d", proc.pid, proc.start)))
	verifiedProcess := func() bool {
		currentLink, err := d.deps.readlink(lock)
		if err != nil || currentLink != link {
			return false
		}
		current, err := d.deps.process(ctx, pid)
		return err == nil && sameChromeProcess(proc, current) && isDefaultChrome(current, pid, d.deps.uid())
	}
	fallback := chromeSession{id: "chrome:" + hex.EncodeToString(sum[:16]), pid: pid, process: proc, verified: verifiedProcess}
	if !verifiedProcess() {
		return chromeSession{}, chromeProcess{}, "", chromeContextError(ctx, ErrChromeIdentityUnverified)
	}
	return fallback, proc, dir, nil
}

func (d *ChromeDiscovery) findChrome(ctx context.Context) (chromeSession, error) {
	fallback, proc, dir, err := d.findChromeProcess(ctx)
	if err != nil {
		return chromeSession{}, err
	}
	deps, pid, verifiedProcess := d.deps, fallback.pid, fallback.verified
	port, ephemeral, ok := chromeDebugPort(proc.args)
	if !ok {
		return fallback, nil
	}
	if ephemeral {
		contents, err := deps.active(filepath.Join(dir, "DevToolsActivePort"))
		if err != nil {
			return fallback, nil
		}
		port, err = strconv.Atoi(strings.TrimSpace(strings.SplitN(contents, "\n", 2)[0]))
		if err != nil || port < 1 || port > 65535 {
			return fallback, nil
		}
	}
	// Revalidate before each request and after the response. Neither a stale
	// SingletonLock nor a port recycled by a different process authorizes a probe.
	verified := func() bool {
		return verifiedProcess() && deps.listener(ctx, pid, port)
	}
	if !verified() || !probeCDPWithClient(ctx, port, deps.client) || !verified() {
		if err := ctx.Err(); err != nil {
			return chromeSession{}, err
		}
		if !verifiedProcess() {
			return chromeSession{}, ErrChromeIdentityUnverified
		}
		return fallback, nil
	}
	return chromeSession{id: fallback.id, pid: pid, port: port, process: proc, verified: verified}, nil
}

func (d *ChromeDiscovery) list(ctx context.Context) (string, []ChromeTab, error) {
	id, tabs, _, err := d.listWithSource(ctx)
	return id, tabs, err
}

func (d *ChromeDiscovery) listWithSource(ctx context.Context) (string, []ChromeTab, ChromeListSource, error) {
	session, err := d.findChrome(ctx)
	if err != nil {
		return "", nil, "", err
	}
	source := ChromeListSourceCDP
	if session.port == 0 {
		source = ChromeListSourceJXA
	}
	id, tabs, err := d.listSession(ctx, session)
	if err != nil {
		return "", nil, "", err
	}
	return id, tabs, source, nil
}

func (d *ChromeDiscovery) listSession(ctx context.Context, session chromeSession) (string, []ChromeTab, error) {
	if session.port == 0 {
		if !session.verified() || !d.deps.identity(ctx, session.process) || !session.verified() {
			return "", nil, chromeContextError(ctx, ErrChromeIdentityUnverified)
		}
		if d.deps.jxa == nil {
			return "", nil, ErrChromeCDPUnavailable
		}
		tabs, err := d.deps.jxa(ctx, session.pid)
		if !session.verified() || !d.deps.identity(ctx, session.process) || !session.verified() {
			return "", nil, chromeContextError(ctx, ErrChromeIdentityUnverified)
		}
		if err != nil {
			return "", nil, err
		}
		for i := range tabs {
			tabs[i].URL = safeChromeURL(tabs[i].URL)
		}
		return session.id, tabs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", session.port), nil)
	if err != nil {
		return "", nil, ErrChromeCDPUnavailable
	}
	if !session.verified() {
		return "", nil, chromeContextError(ctx, ErrChromeCDPUnavailable)
	}
	resp, err := d.deps.client.Do(req)
	if err != nil {
		return "", nil, chromeContextError(ctx, ErrChromeCDPUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, ErrChromeCDPUnavailable
	}
	const maxResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse || !session.verified() {
		return "", nil, chromeContextError(ctx, ErrChromeCDPUnavailable)
	}
	var targets []struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if json.Unmarshal(body, &targets) != nil || targets == nil {
		return "", nil, ErrChromeCDPUnavailable
	}
	tabs := make([]ChromeTab, 0, len(targets))
	for _, target := range targets {
		if target.Type == "page" && target.ID != "" {
			tabs = append(tabs, ChromeTab{ID: target.ID, Title: target.Title, URL: safeChromeURL(target.URL)})
		}
	}
	return session.id, tabs, nil
}

func chromeContextError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}

func chromeLockPID(link, host string) (int, bool) {
	prefix := host + "-"
	if !strings.HasPrefix(link, prefix) {
		return 0, false
	}
	raw := strings.TrimPrefix(link, prefix)
	pid, err := strconv.Atoi(raw)
	return pid, err == nil && pid > 0 && strconv.Itoa(pid) == raw
}

func isDefaultChrome(p chromeProcess, pid, uid int) bool {
	if p.pid != pid || p.ppid != 1 || p.uid != uid || p.start == 0 || len(p.args) == 0 ||
		!filepath.IsAbs(p.exe) || !strings.HasSuffix(p.exe, "/Google Chrome.app/Contents/MacOS/Google Chrome") {
		return false
	}
	for _, arg := range p.args[1:] {
		if arg == "--user-data-dir" || strings.HasPrefix(arg, "--user-data-dir=") ||
			arg == "--type" || strings.HasPrefix(arg, "--type=") {
			return false
		}
	}
	return true
}

func sameChromeProcess(a, b chromeProcess) bool {
	if a.pid != b.pid || a.ppid != b.ppid || a.start != b.start || a.uid != b.uid || a.exe != b.exe || len(a.args) != len(b.args) {
		return false
	}
	for i := range a.args {
		if a.args[i] != b.args[i] {
			return false
		}
	}
	return true
}

func chromeDebugPort(args []string) (port int, ephemeral, ok bool) {
	found := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		var value string
		switch {
		case arg == "--remote-debugging-port":
			if i+1 >= len(args) {
				return 0, false, false
			}
			i++
			value = args[i]
		case strings.HasPrefix(arg, "--remote-debugging-port="):
			value = strings.TrimPrefix(arg, "--remote-debugging-port=")
		default:
			continue
		}
		if found || value == "" {
			return 0, false, false
		}
		found = true
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 65535 || strconv.Itoa(n) != value || !(Process{Args: "--remote-debugging-port=" + value}).debugs(n) {
			return 0, false, false
		}
		port = n
	}
	return port, found && port == 0, found
}

func safeChromeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

func readChromeActivePort(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("invalid DevToolsActivePort")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("DevToolsActivePort changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return "", errors.New("invalid DevToolsActivePort")
	}
	return string(b), nil
}

func localChromeHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil || host != "127.0.0.1" || network != "tcp" {
				return nil, errors.New("non-local DevTools address")
			}
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", address)
		},
	}
	return &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}
