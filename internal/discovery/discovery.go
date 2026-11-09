// Package discovery enumerates listening TCP sockets and the processes that
// own them by reading /proc, without any network scanning.
package discovery

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Listener is one listening TCP socket found in /proc/net/{tcp,tcp6}.
type Listener struct {
	IP    net.IP
	Port  int
	Inode string
}

// Host returns the bare hostname to use when connecting to this listener;
// bracketing for IPv6 is applied by net.JoinHostPort.
func (l Listener) Host() string {
	if l.IP.IsUnspecified() {
		if l.IP.To4() == nil {
			return "::1"
		}
		return "127.0.0.1"
	}
	return l.IP.String()
}

func (l Listener) Addr() string {
	return net.JoinHostPort(l.Host(), strconv.Itoa(l.Port))
}

// Listeners returns every listening TCP socket visible to this process.
func Listeners() ([]Listener, error) {
	var out []Listener
	seen := map[string]bool{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if i == 0 || line == "" {
				continue // header
			}
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			if fields[3] != "0A" { // 0A == LISTEN
				continue
			}
			ip, port, err := parseAddr(fields[1])
			if err != nil {
				continue
			}
			inode := fields[9]
			key := ip.String() + ":" + strconv.Itoa(port)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Listener{IP: ip, Port: port, Inode: inode})
		}
	}
	return out, nil
}

// parseAddr decodes /proc/net/tcp "local_address" fields like
// "0100007F:1F90" (IPv4) or 32 hex chars for IPv6. Both store the address as
// four little-endian 32-bit words, so each word must be byte-swapped.
func parseAddr(s string) (net.IP, int, error) {
	colon := strings.LastIndex(s, ":")
	if colon < 0 {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	port, err := strconv.ParseUint(s[colon+1:], 16, 16)
	if err != nil {
		return nil, 0, err
	}
	hex := s[:colon]
	raw, err := unhex(hex)
	if err != nil {
		return nil, 0, err
	}
	// Byte-swap each 4-byte word to get real network byte order.
	for i := 0; i+3 < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return net.IP(raw), int(port), nil
}

func unhex(hex string) ([]byte, error) {
	if len(hex)%2 != 0 {
		return nil, fmt.Errorf("odd hex %q", hex)
	}
	out := make([]byte, len(hex)/2)
	for i := range out {
		hi, ok1 := fromHexDigit(hex[i*2])
		lo, ok2 := fromHexDigit(hex[i*2+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("bad hex %q", hex)
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func fromHexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// Process identifies the process holding a socket.
type Process struct {
	PID     int
	Name    string
	Cmdline string
	RSSKB   int
}

// Mem returns RSS as a human-readable string, e.g. "1.2 GiB".
func (p Process) Mem() string {
	if p.RSSKB <= 0 {
		return "-"
	}
	const kib = 1024
	mib := float64(p.RSSKB) / kib
	switch {
	case mib >= kib:
		return fmt.Sprintf("%.2f GiB", mib/kib)
	case mib >= 1:
		return fmt.Sprintf("%.1f MiB", mib)
	default:
		return fmt.Sprintf("%d KiB", p.RSSKB)
	}
}

// Owners maps socket inodes to the processes holding them.
// key: inode string as it appears in /proc/net/tcp, e.g. "12345".
func Owners() (map[string]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	owners := map[string]Process{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		inodes, err := socketInodes(pid)
		if err != nil || len(inodes) == 0 {
			continue
		}
		proc := readProcess(pid)
		for _, ino := range inodes {
			if _, ok := owners[ino]; !ok {
				owners[ino] = proc
			}
		}
	}
	return owners, nil
}

func socketInodes(pid int) ([]string, error) {
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return nil, err
	}
	var inodes []string
	for _, fd := range fds {
		link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if err != nil {
			continue
		}
		if ino, ok := strings.CutPrefix(link, "socket:["); ok {
			inodes = append(inodes, strings.TrimSuffix(ino, "]"))
		}
	}
	return inodes, nil
}

func readProcess(pid int) Process {
	p := Process{PID: pid}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		p.Name = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		p.Cmdline = strings.ReplaceAll(string(b), "\x00", " ")
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				fields := strings.Fields(v)
				if len(fields) >= 1 {
					if kb, err := strconv.Atoi(fields[0]); err == nil {
						p.RSSKB = kb
					}
				}
				break
			}
		}
	}
	if p.Name == "" {
		p.Name = strconv.Itoa(pid)
	}
	return p
}

var _ = binary.LittleEndian // reserved for future u32 word decoding
