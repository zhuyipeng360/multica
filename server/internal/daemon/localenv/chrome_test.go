package localenv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const testChromeHome = "/Users/test"

func fakeChromeDiscovery(port int) *ChromeDiscovery {
	p := chromeProcess{pid: 123, ppid: 1, uid: 501, start: 42,
		exe:  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		args: []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "--remote-debugging-port=" + strconv.Itoa(port)}}
	return &ChromeDiscovery{deps: chromeDependencies{
		home: func() (string, error) { return testChromeHome, nil }, hostname: func() (string, error) { return "test-host", nil },
		uid: func() int { return 501 }, readlink: func(string) (string, error) { return "test-host-123", nil },
		process:  func(context.Context, int) (chromeProcess, error) { return p, nil },
		identity: func(context.Context, chromeProcess) bool { return true },
		listener: func(_ context.Context, pid, gotPort int) bool { return pid == 123 && gotPort == port },
		active:   func(string) (string, error) { return fmt.Sprintf("%d\n/devtools/browser/test", port), nil },
		client:   localChromeHTTPClient(),
	}}
}

func testCDP(t *testing.T, list string) (*httptest.Server, *ChromeDiscovery) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			fmt.Fprint(w, `{"Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/browser/test"}`)
		case "/json/list":
			fmt.Fprint(w, list)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s, fakeChromeDiscovery(s.Listener.Addr().(*net.TCPAddr).Port)
}

func TestChromeListTabs(t *testing.T) {
	_, d := testCDP(t, `[{"type":"page","id":"1","title":"A","url":"https://user:pass@example.com/path?token=secret#private","webSocketDebuggerUrl":"ws://secret"},{"type":"worker","id":"2","title":"worker","url":"https://example.com"},{"type":"page","id":"3","title":"local","url":"file:///Users/test/secret"},{"type":"page","id":"4","title":"new tab","url":"chrome://newtab/"}]`)
	tabs, err := d.listTabs(context.Background())
	want := []ChromeTab{{ID: "1", Title: "A", URL: "https://example.com/path"}, {ID: "3", Title: "local"}, {ID: "4", Title: "new tab"}}
	if err != nil || !reflect.DeepEqual(tabs, want) {
		t.Fatalf("listTabs = %#v, %v; want %#v", tabs, err, want)
	}
}

func TestChromeDiscoverDoesNotReadTabsAndChangesID(t *testing.T) {
	var versions, lists atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			versions.Add(1)
			fmt.Fprint(w, `{"Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/browser/test"}`)
		case "/json/list":
			lists.Add(1)
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer s.Close()
	d := fakeChromeDiscovery(s.Listener.Addr().(*net.TCPAddr).Port)
	ctx := context.Background()
	id, err := d.discover(ctx)
	if err != nil || id == "" || versions.Load() != 1 || lists.Load() != 0 {
		t.Fatalf("discover = %q, %v; versions=%d lists=%d", id, err, versions.Load(), lists.Load())
	}
	listID, _, err := d.list(ctx)
	if err != nil || listID != id || lists.Load() != 1 {
		t.Fatalf("list = %q, %v, lists=%d", listID, err, lists.Load())
	}
	editFakeProcess(func(p *chromeProcess) { p.start++ })(&d.deps)
	newID, err := d.discover(ctx)
	if err != nil || newID == id || newID == "" {
		t.Fatalf("new instance ID = %q, %v; previous %q", newID, err, id)
	}
}

func TestChromeDiscoveryRejectsUnverifiedOwners(t *testing.T) {
	cases := []struct {
		name string
		edit func(*chromeDependencies)
	}{
		{"missing lock", func(d *chromeDependencies) { d.readlink = func(string) (string, error) { return "", os.ErrNotExist } }},
		{"other host", func(d *chromeDependencies) { d.readlink = func(string) (string, error) { return "other-123", nil } }},
		{"malformed lock", func(d *chromeDependencies) {
			d.readlink = func(string) (string, error) { return "test-host-0123", nil }
		}},
		{"no process", func(d *chromeDependencies) {
			d.process = func(context.Context, int) (chromeProcess, error) { return chromeProcess{}, os.ErrNotExist }
		}},
		{"other user", editFakeProcess(func(p *chromeProcess) { p.uid++ })},
		{"reused pid", editFakeProcess(func(p *chromeProcess) { p.pid++ })},
		{"koko", editFakeProcess(func(p *chromeProcess) { p.exe = "/Applications/Koko.app/Contents/MacOS/Koko" })},
		{"koko parent", editFakeProcess(func(p *chromeProcess) { p.ppid = 42 })},
		{"invalid signature", func(d *chromeDependencies) { d.identity = func(context.Context, chromeProcess) bool { return false } }},
		{"chrome helper", editFakeProcess(func(p *chromeProcess) { p.args = append(p.args, "--type=renderer") })},
		{"explicit default directory", editFakeProcess(func(p *chromeProcess) {
			p.args = append(p.args, "--user-data-dir=/Users/test/Library/Application Support/Google/Chrome")
		})},
		{"explicit alternate directory", editFakeProcess(func(p *chromeProcess) { p.args = append(p.args, "--user-data-dir", "/tmp/koko") })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := fakeChromeDiscovery(12345)
			tc.edit(&d.deps)
			d.deps.listener = func(context.Context, int, int) bool { t.Error("queried listener for unverified owner"); return false }
			_, err := d.listTabs(context.Background())
			want := ErrChromeIdentityUnverified
			if tc.name == "missing lock" {
				want = ErrChromeNotRunning
			}
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func editFakeProcess(edit func(*chromeProcess)) func(*chromeDependencies) {
	return func(d *chromeDependencies) {
		original := d.process
		d.process = func(ctx context.Context, pid int) (chromeProcess, error) {
			p, err := original(ctx, pid)
			p.args = append([]string(nil), p.args...)
			edit(&p)
			return p, err
		}
	}
}

func TestChromeAcceptsVerifiedRelocatedBundle(t *testing.T) {
	_, d := testCDP(t, `[]`)
	editFakeProcess(func(p *chromeProcess) {
		p.exe = "/Users/test/Library/Application Support/Koko/runtime/chrome/Google Chrome.app/Contents/MacOS/Google Chrome"
	})(&d.deps)
	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("verified relocated Chrome = %v", err)
	}
}

func TestChromeDebugPort(t *testing.T) {
	for _, tc := range []struct {
		args          []string
		port          int
		ephemeral, ok bool
	}{
		{[]string{"chrome"}, 0, false, false},
		{[]string{"chrome", "--remote-debugging-port=9222"}, 9222, false, true},
		{[]string{"chrome", "--remote-debugging-port", "1234"}, 1234, false, true},
		{[]string{"chrome", "--remote-debugging-port=0"}, 0, true, true},
		{[]string{"chrome", "--remote-debugging-port=-1"}, 0, false, false},
		{[]string{"chrome", "--remote-debugging-port=65536"}, 0, false, false},
		{[]string{"chrome", "--remote-debugging-port=9222junk"}, 0, false, false},
		{[]string{"chrome", "--remote-debugging-port=01"}, 0, false, false},
		{[]string{"chrome", "--remote-debugging-port=1", "--remote-debugging-port=2"}, 0, false, false},
		{[]string{"chrome", "--remote-debugging-port"}, 0, false, false},
	} {
		p, ephemeral, ok := chromeDebugPort(tc.args)
		if p != tc.port || ephemeral != tc.ephemeral || ok != tc.ok {
			t.Errorf("chromeDebugPort(%q) = %d, %v, %v", tc.args, p, ephemeral, ok)
		}
	}
}

func TestChromeEphemeralPort(t *testing.T) {
	_, d := testCDP(t, `[]`)
	original := d.deps.process
	d.deps.process = func(ctx context.Context, pid int) (chromeProcess, error) {
		p, err := original(ctx, pid)
		p.args = []string{p.exe, "--remote-debugging-port=0"}
		return p, err
	}
	if tabs, err := d.listTabs(context.Background()); err != nil || len(tabs) != 0 {
		t.Fatalf("ephemeral port = %v, %v", tabs, err)
	}
	d.deps.active = func(string) (string, error) { return "0\n/devtools/browser/id", nil }
	if _, err := d.listTabs(context.Background()); !errors.Is(err, ErrChromeCDPUnavailable) {
		t.Fatalf("invalid ephemeral port = %v", err)
	}
}

func TestChromeDoesNotProbeUnownedOrChangedPort(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer s.Close()
	port := s.Listener.Addr().(*net.TCPAddr).Port
	t.Run("unowned", func(t *testing.T) {
		d := fakeChromeDiscovery(port)
		d.deps.listener = func(context.Context, int, int) bool { return false }
		_, err := d.listTabs(context.Background())
		if !errors.Is(err, ErrChromeCDPUnavailable) || requests.Load() != 0 {
			t.Fatalf("err=%v requests=%d", err, requests.Load())
		}
	})
	t.Run("pid reused", func(t *testing.T) {
		d := fakeChromeDiscovery(port)
		original := d.deps.process
		var calls int
		d.deps.process = func(ctx context.Context, pid int) (chromeProcess, error) {
			p, err := original(ctx, pid)
			calls++
			if calls > 1 {
				p.start++
			}
			return p, err
		}
		_, err := d.listTabs(context.Background())
		if !errors.Is(err, ErrChromeCDPUnavailable) || requests.Load() != 0 {
			t.Fatalf("err=%v requests=%d", err, requests.Load())
		}
	})
	t.Run("lock changed", func(t *testing.T) {
		d := fakeChromeDiscovery(port)
		var calls int
		d.deps.readlink = func(string) (string, error) {
			calls++
			if calls > 1 {
				return "test-host-456", nil
			}
			return "test-host-123", nil
		}
		_, err := d.listTabs(context.Background())
		if !errors.Is(err, ErrChromeCDPUnavailable) || requests.Load() != 0 {
			t.Fatalf("err=%v requests=%d", err, requests.Load())
		}
	})
}

func TestChromeEndpointFailures(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `invalid`, strings.Repeat("x", (1<<20)+1)} {
		t.Run(fmt.Sprintf("body length %d", len(body)), func(t *testing.T) {
			_, d := testCDP(t, body)
			if _, err := d.listTabs(context.Background()); !errors.Is(err, ErrChromeCDPUnavailable) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	_, d := testCDP(t, `[]`)
	d.deps.process = func(context.Context, int) (chromeProcess, error) {
		return chromeProcess{pid: 123, ppid: 1, uid: 501, start: 42, exe: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", args: []string{"chrome"}}, nil
	}
	if _, err := d.listTabs(context.Background()); !errors.Is(err, ErrChromeCDPUnavailable) {
		t.Fatalf("no debugging = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.listTabs(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
}

func TestChromeHTTPNoRedirectOrProxy(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer target.Close()
	t.Setenv("HTTP_PROXY", target.URL)
	t.Setenv("ALL_PROXY", target.URL)
	t.Setenv("NO_PROXY", "")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer s.Close()
	d := fakeChromeDiscovery(s.Listener.Addr().(*net.TCPAddr).Port)
	if _, err := d.listTabs(context.Background()); !errors.Is(err, ErrChromeCDPUnavailable) || reached.Load() != 0 {
		t.Fatalf("error=%v redirected requests=%d", err, reached.Load())
	}
	resp, err := localChromeHTTPClient().Get("http://example.com/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("non-local request succeeded")
	}
}

func TestChromeSafeURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://u:p@example.com/a?x=secret#fragment": "https://example.com/a",
		"http://localhost:8080/?":                     "http://localhost:8080/",
		"file:///home/user/a":                         "", "data:text/plain,secret": "", "chrome://settings": "", "https://": "",
		"https://example.com/%zz": "",
	} {
		if got := safeChromeURL(raw); got != want {
			t.Errorf("safeChromeURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestChromeReadActivePort(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "port")
	if err := os.WriteFile(file, []byte("9222\n/devtools/browser/id"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readChromeActivePort(file); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readChromeActivePort(link); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := os.WriteFile(file, []byte(strings.Repeat("1", 4097)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readChromeActivePort(file); err == nil {
		t.Fatal("accepted oversized file")
	}
}

func TestChromeConcurrentList(t *testing.T) {
	_, d := testCDP(t, `[{"type":"page","id":"one","title":"one","url":"https://example.com"}]`)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			tabs, err := d.listTabs(context.Background())
			if err != nil || len(tabs) != 1 {
				t.Errorf("list = %v, %v", tabs, err)
			}
		})
	}
	wg.Wait()
}
