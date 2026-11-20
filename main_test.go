package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestParse(t *testing.T) {
	mode, n, err := parseArgs(nil)
	if err != nil || mode != "mix" || n != 10 {
		t.Fatalf("default: %s %d %v", mode, n, err)
	}
	mode, n, err = parseArgs([]string{"chat", "7"})
	if err != nil || mode != "chat" || n != 7 {
		t.Fatalf("chat 7: %s %d %v", mode, n, err)
	}
	mode, n, err = parseArgs([]string{"20", "shell"})
	if err != nil || mode != "shell" || n != 20 {
		t.Fatalf("20 shell: %s %d %v", mode, n, err)
	}
	mode, n, err = parseArgs([]string{"demo", "20"})
	if err != nil || mode != "demo" || n != 3 {
		t.Fatalf("demo: %s %d %v", mode, n, err)
	}
	if _, _, err = parseArgs([]string{"nope"}); err == nil {
		t.Fatal("expected an error for nope")
	}
	if _, _, err = parseArgs([]string{"0"}); err == nil {
		t.Fatal("expected an error for 0")
	}
	if _, _, err = parseArgs([]string{"101"}); err == nil {
		t.Fatal("expected an error for 101")
	}
	if _, _, err = parseArgs([]string{"-h"}); !errors.Is(err, errHelp) {
		t.Fatal(err)
	}
}

func TestLines(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range append(append([]string{}, shellLines...), chatLines...) {
		if s == "" {
			t.Fatal("empty line")
		}
		if len([]rune(s)) > 64 {
			t.Fatalf("longer than 64: %q (%d)", s, len([]rune(s)))
		}
		if seen[s] {
			t.Fatalf("duplicate: %q", s)
		}
		seen[s] = true
	}
	shell := strings.Join(shellLines, "\n")
	for _, sym := range []string{"|", ">", "<", "~", "/", "-", "'", `"`, "$", "&", ";", `\`, "*", "%"} {
		if !strings.Contains(shell, sym) {
			t.Errorf("shell lines missing %q", sym)
		}
	}
	chat := strings.Join(chatLines, "\n")
	for _, sym := range []string{"'", ",", ".", "?", `"`, "-", ":", "(", "/"} {
		if !strings.Contains(chat, sym) {
			t.Errorf("chat lines missing %q", sym)
		}
	}
}

func TestDemoLines(t *testing.T) {
	shell := map[string]bool{}
	for _, s := range shellLines {
		shell[s] = true
	}
	chat := map[string]bool{}
	for _, s := range chatLines {
		chat[s] = true
	}
	for _, p := range demoPrompts() {
		switch p.kind {
		case "shell":
			if !shell[p.text] {
				t.Fatalf("demo shell line not in the bank: %q", p.text)
			}
		case "chat":
			if !chat[p.text] {
				t.Fatalf("demo chat line not in the bank: %q", p.text)
			}
		default:
			t.Fatalf("kind %s", p.kind)
		}
	}
	got := stripANSI(paintTarget("echo", []rune("ec")))
	if strings.TrimSpace(got) != "echo" {
		t.Fatalf("paint: %q", got)
	}
}

func TestPick(t *testing.T) {
	got := pick("mix", 40, 80)
	if len(got) != 40 {
		t.Fatal(len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].text == got[i-1].text {
			t.Fatalf("immediate repeat: %q", got[i].text)
		}
	}
	shells := pick("shell", 5, 80)
	for _, p := range shells {
		if p.kind != "shell" {
			t.Fatalf("kind %s", p.kind)
		}
	}
}

func TestStatFormat(t *testing.T) {
	s := stat{runes: 25, correct: 25, wrong: 0, elapsed: 6 * time.Second}
	if s.wpm() != 50 {
		t.Fatal(s.wpm())
	}
	if s.String() != "50 wpm · 100% · 6.0s" {
		t.Fatal(s.String())
	}
	s = stat{runes: 20, correct: 20, wrong: 1, elapsed: 12 * time.Second}
	if s.wpm() != 20 || s.accuracy() != 95 {
		t.Fatalf("wpm %d acc %d", s.wpm(), s.accuracy())
	}
	if s.String() != "20 wpm · 95% · 1 miss · 12.0s" {
		t.Fatal(s.String())
	}
	s = stat{runes: 10, correct: 10, wrong: 2, elapsed: 65 * time.Second}
	if !strings.Contains(s.String(), "2 misses") || !strings.Contains(s.String(), "1m05s") {
		t.Fatal(s.String())
	}
	if sessionLine(nil) != "  no finished lines" {
		t.Fatal(sessionLine(nil))
	}
	line := sessionLine([]stat{
		{runes: 25, correct: 25, wrong: 0, elapsed: 6 * time.Second},
		{runes: 25, correct: 24, wrong: 1, elapsed: 6 * time.Second},
	})
	if !strings.HasPrefix(line, "  2 lines · ") || !strings.Contains(line, "1 miss") {
		t.Fatal(line)
	}
}

func TestPlay(t *testing.T) {
	var buf bytes.Buffer
	keys := sliceKeys([]rune{'a', 'x', keyBack, 'b'})
	st, stop := play(&buf, keys, prompt{kind: "shell", text: "ab"}, 1, 3)
	if stop || st == nil {
		t.Fatal("expected a finished line")
	}
	if st.wrong != 1 || st.correct != 2 || st.runes != 2 {
		t.Fatalf("%+v", st)
	}
	out := buf.String()
	if !strings.Contains(out, "shell  1/3") || !strings.Contains(out, "ab") || !strings.Contains(out, "1 miss") {
		t.Fatal(out)
	}

	buf.Reset()
	st, stop = play(&buf, sliceKeys([]rune{'a', keyQuit}), prompt{kind: "chat", text: "ab"}, 1, 1)
	if !stop || st != nil {
		t.Fatalf("quit: stop=%v st=%v", stop, st)
	}

	buf.Reset()
	st, stop = play(&buf, sliceKeys([]rune{'\r', 'a', 'b'}), prompt{kind: "chat", text: "ab"}, 2, 4)
	if stop || st == nil || st.wrong != 0 {
		t.Fatalf("enter ignored: stop=%v st=%+v", stop, st)
	}
}

func sliceKeys(keys []rune) func() (rune, error) {
	i := 0
	return func() (rune, error) {
		if i >= len(keys) {
			return 0, io.EOF
		}
		k := keys[i]
		i++
		return k, nil
	}
}

func TestCLI(t *testing.T) {
	bin := buildBin(t)
	master, slave := openPTY(t)
	defer master.Close()
	defer slave.Close()

	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 100}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "shell", "1")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The child has its own copy of the slave.
	_ = slave.Close()

	var (
		mu  sync.Mutex
		out bytes.Buffer
	)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 1024)
		for {
			_ = master.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, err := master.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
				return
			}
		}
	}()

	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}

	var target string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		target = targetFrom(snapshot())
		if target != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if target == "" {
		cmd.Process.Kill()
		t.Fatalf("no target in output:\n%s", snapshot())
	}

	miss := byte('Z')
	if target[0] == 'Z' {
		miss = 'Q'
	}
	payload := append([]byte{miss, 0x7f}, target...)
	if _, err := master.Write(payload); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(snapshot(), "1 miss") && strings.Contains(snapshot(), "wpm") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(snapshot(), "1 miss") {
		cmd.Process.Kill()
		t.Fatalf("target %q\noutput:\n%s", target, snapshot())
	}

	waitErr := waitCmd(cmd, 2*time.Second)
	_ = master.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("reader did not finish")
	}
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	final := stripANSI(snapshot())
	if strings.Count(final, "wpm") < 2 {
		t.Fatalf("summary missing:\n%s", final)
	}

	// A second run checks ctrl-c and that the terminal is cooked again.
	master2, slave2 := openPTY(t)
	defer master2.Close()
	defer slave2.Close()
	cmd = exec.Command(bin, "chat", "3")
	cmd.Stdin = slave2
	cmd.Stdout = slave2
	cmd.Stderr = slave2
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var out2 bytes.Buffer
	var mu2 sync.Mutex
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		buf := make([]byte, 1024)
		for {
			_ = master2.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, err := master2.Read(buf)
			if n > 0 {
				mu2.Lock()
				out2.Write(buf[:n])
				mu2.Unlock()
			}
			if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
				return
			}
		}
	}()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu2.Lock()
		got := targetFrom(out2.String())
		mu2.Unlock()
		if got != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := master2.Write([]byte{0x03}); err != nil {
		t.Fatal(err)
	}
	if err := waitCmd(cmd, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := unix.IoctlGetTermios(int(slave2.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lflag&unix.ECHO == 0 || got.Lflag&unix.ICANON == 0 {
		t.Fatalf("terminal left raw: lflag=%#x", got.Lflag)
	}
	_ = slave2.Close()
	_ = master2.Close()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("reader did not finish")
	}
	mu2.Lock()
	text := stripANSI(out2.String())
	mu2.Unlock()
	if !strings.Contains(text, "no finished lines") {
		t.Fatalf("ctrl-c summary:\n%s", text)
	}
}

func TestCLIArgs(t *testing.T) {
	bin := buildBin(t)

	cmd := exec.Command(bin, "nope")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected exit error")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "unknown argument") {
		t.Fatal(string(out))
	}

	cmd = exec.Command(bin, "-h")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	if !strings.Contains(string(out), "clitype chat") {
		t.Fatal(string(out))
	}

	cmd = exec.Command(bin)
	cmd.Stdin = strings.NewReader("")
	cmd.Stdout = &bytes.Buffer{}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		t.Fatal("expected non-tty to fail")
	}
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "needs a terminal") {
		t.Fatal(stderr.String())
	}
}

func buildBin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "clitype")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skip(err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	return m, s
}

func waitCmd(cmd *exec.Cmd, d time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		_ = cmd.Process.Kill()
		<-done
		return errors.New("process timed out")
	}
}

func targetFrom(out string) string {
	lines := splitLines(stripANSI(out))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) < 6 {
			continue
		}
		kind, rest, ok := strings.Cut(line, " ")
		if !ok || (kind != "shell" && kind != "chat") {
			continue
		}
		rest = strings.TrimSpace(rest)
		if !strings.Contains(rest, "/") {
			continue
		}
		for _, next := range lines[i+1:] {
			next = strings.TrimSpace(next)
			if next != "" && next != kind {
				return next
			}
		}
	}
	return ""
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
