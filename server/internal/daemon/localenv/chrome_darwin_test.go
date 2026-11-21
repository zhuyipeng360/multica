//go:build darwin

package localenv

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestChromeParseProcArgs(t *testing.T) {
	var raw bytes.Buffer
	if err := binary.Write(&raw, binary.NativeEndian, uint32(3)); err != nil {
		t.Fatal(err)
	}
	raw.WriteString("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome\x00\x00\x00")
	raw.WriteString("Google Chrome\x00--remote-debugging-port=9222\x00--name=with space\x00ENV=secret\x00")
	exe, args, err := parseChromeProcArgs(raw.Bytes())
	if err != nil || !strings.HasSuffix(exe, "/Google Chrome") || len(args) != 3 || args[2] != "--name=with space" {
		t.Fatalf("parse = %q %q %v", exe, args, err)
	}
	if _, _, err := parseChromeProcArgs(raw.Bytes()[:15]); err == nil {
		t.Fatal("accepted truncated args")
	}
}

func TestChromeCurrentProcessInfo(t *testing.T) {
	p, err := chromeProcessInfo(context.Background(), os.Getpid())
	if err != nil || p.pid != os.Getpid() || p.uid != os.Getuid() || p.start == 0 || len(p.args) == 0 {
		t.Fatalf("current process = %+v, %v", p, err)
	}
}

func TestChromeLsofOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      bool
	}{
		{"exact", "p123\nn127.0.0.1:9222\n", true},
		{"different pid", "p456\nn127.0.0.1:9222\n", false},
		{"different port", "p123\nn127.0.0.1:9223\n", false},
		{"wildcard", "p123\nn*:9222\n", false},
		{"other address", "p123\nn192.168.1.1:9222\n", false},
		{"extra process", "p123\nn127.0.0.1:9222\np456\nn127.0.0.1:9222\n", false},
		{"no pid", "n127.0.0.1:9222\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := chromeLsofOwnsLoopback(tc.out, 123, 9222); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestChromeLsofRealLoopbackOwnership(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer s.Close()
	port := s.Listener.Addr().(*net.TCPAddr).Port
	if !chromeOwnsListener(context.Background(), os.Getpid(), port) {
		t.Fatal("failed to recognize own httptest listener on " + strconv.Itoa(port))
	}
	if chromeOwnsListener(context.Background(), os.Getppid(), port) {
		t.Fatal("attributed listener to another pid")
	}
	if chromeOwnsListener(context.Background(), os.Getpid(), port+1) {
		t.Fatal("attributed a different port")
	}
}
