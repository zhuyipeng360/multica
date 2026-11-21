//go:build darwin

package localenv

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const chromeSupported = true

// KERN_PROCARGS2 returns the executable and unambiguous NUL-separated argv;
// ps's flattened command line cannot distinguish arguments containing spaces.
func chromeProcessInfo(ctx context.Context, pid int) (chromeProcess, error) {
	if err := ctx.Err(); err != nil {
		return chromeProcess{}, err
	}
	before, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || before.Proc.P_pid != int32(pid) || before.Proc.P_stat == 5 {
		return chromeProcess{}, errors.New("process unavailable")
	}
	argmax, err := unix.SysctlUint32("kern.argmax")
	if err != nil || argmax == 0 || argmax > 8<<20 {
		return chromeProcess{}, errors.New("invalid process argument limit")
	}
	buf := make([]byte, argmax)
	n := uintptr(len(buf))
	mib := [3]int32{unix.CTL_KERN, 49, int32(pid)} // KERN_PROCARGS2
	_, _, errno := syscall.Syscall6(unix.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if errno != 0 || n > uintptr(len(buf)) {
		return chromeProcess{}, errors.New("process arguments unavailable")
	}
	exe, args, err := parseChromeProcArgs(buf[:n])
	if err != nil {
		return chromeProcess{}, err
	}
	after, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || after.Proc.P_pid != int32(pid) || after.Proc.P_stat == 5 ||
		after.Proc.P_starttime != before.Proc.P_starttime || after.Eproc.Ucred.Uid != before.Eproc.Ucred.Uid || after.Eproc.Ppid != before.Eproc.Ppid {
		return chromeProcess{}, errors.New("process changed")
	}
	return chromeProcess{
		pid: pid, ppid: int(before.Eproc.Ppid), start: uint64(before.Proc.P_starttime.Sec)*1_000_000 + uint64(before.Proc.P_starttime.Usec),
		uid: int(before.Eproc.Ucred.Uid), exe: exe, args: args,
	}, nil
}

func parseChromeProcArgs(data []byte) (string, []string, error) {
	if len(data) < 5 {
		return "", nil, errors.New("short process arguments")
	}
	argc := int(binary.NativeEndian.Uint32(data[:4]))
	data = data[4:]
	end := bytes.IndexByte(data, 0)
	if end <= 0 || argc <= 0 || argc > 65536 {
		return "", nil, errors.New("invalid process arguments")
	}
	exe := string(data[:end])
	data = bytes.TrimLeft(data[end:], "\x00")
	args := make([]string, 0, argc)
	for range argc {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return "", nil, errors.New("truncated process arguments")
		}
		args = append(args, string(data[:end]))
		data = data[end+1:]
	}
	return exe, args, nil
}

// Verify the actual signed bundle, not display-only signature metadata.
func verifyChromeSignature(ctx context.Context, p chromeProcess) bool {
	const executable = "/Contents/MacOS/Google Chrome"
	if !strings.HasSuffix(p.exe, executable) {
		return false
	}
	bundle := strings.TrimSuffix(p.exe, executable)
	const requirement = `=anchor apple generic and identifier "com.google.Chrome" and certificate leaf[subject.OU] = "EQHXZ8M8AV"`
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--requirement", requirement, bundle).Run() == nil
}

// Query only the candidate PID and port. -a is essential: without it lsof
// ORs selectors and can attribute an unrelated process's socket to Chrome.
func chromeOwnsListener(ctx context.Context, pid, port int) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-i4TCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpn").Output()
	if err != nil {
		return false
	}
	return chromeLsofOwnsLoopback(string(out), pid, port)
}

func chromeLsofOwnsLoopback(out string, pid, port int) bool {
	want := "127.0.0.1:" + strconv.Itoa(port)
	current := 0
	found := false
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			current, _ = strconv.Atoi(line[1:])
		case 'n':
			// Refuse wildcard and non-loopback sockets even if another matching
			// record is present; ownership and binding must both be certain.
			if current != pid || line[1:] != want {
				return false
			}
			found = true
		}
	}
	return found
}
