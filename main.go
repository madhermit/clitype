// Command clitype is a terminal typing drill.
// It shows a shell line or a message like one you would type to an LLM,
// lets you type it, and prints a short stat line when you finish.
package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	keyQuit rune = utf8.MaxRune + iota + 1
	keyIgnore
	keyBack
)

type prompt struct {
	kind string
	text string
}

type stat struct {
	runes   int
	correct int
	wrong   int
	elapsed time.Duration
}

func main() {
	mode, n, err := parseArgs(os.Args[1:])
	if errors.Is(err, errHelp) {
		usage(os.Stdout)
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage(os.Stderr)
		os.Exit(2)
	}

	inFD := int(os.Stdin.Fd())
	outFD := int(os.Stdout.Fd())
	if !term.IsTerminal(inFD) || !term.IsTerminal(outFD) {
		fmt.Fprintln(os.Stderr, "clitype needs a terminal")
		os.Exit(1)
	}

	prompts := pick(mode, n, termWidth(outFD))
	if len(prompts) == 0 {
		fmt.Fprintln(os.Stderr, "no lines to practice")
		os.Exit(1)
	}

	fmt.Printf("\033[2m  %s\033[0m\n\n", intro(mode, len(prompts)))

	old, err := term.MakeRaw(inFD)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		_ = term.Restore(inFD, old)
		fmt.Print("\033[0m\033[?25h")
	}
	defer restore()
	fmt.Print("\033[?25l")

	var done []stat
	for i, p := range prompts {
		st, stop := play(os.Stdout, readKey, p, i+1, len(prompts))
		if st != nil {
			done = append(done, *st)
		}
		if stop {
			break
		}
	}

	restore()
	fmt.Println()
	fmt.Println(sessionLine(done))
}

func intro(mode string, n int) string {
	return mode + " · " + linesWord(n) + " · nothing is run · backspace fixes · ctrl-c stops"
}

func usage(w io.Writer) {
	fmt.Fprint(w, `clitype [shell|chat|mix|demo] [count]

Practice typing in this terminal. Nothing you type is executed.

  shell   commands, flags, paths, and pipes
  chat    messages like the ones you type to an LLM
  mix     both (the default)
  demo    three fixed lines

count is how many lines, from 1 to 100. the default is 10.
demo always runs its three lines.

  clitype
  clitype chat
  clitype shell 20
  clitype demo
`)
}

var errHelp = errors.New("help")

func parseArgs(args []string) (string, int, error) {
	mode := "mix"
	n := 10
	seenN := false
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			return "", 0, errHelp
		case "shell", "chat", "mix", "demo":
			mode = a
		default:
			v, err := strconv.Atoi(a)
			if err != nil || seenN {
				return "", 0, fmt.Errorf("unknown argument %q", a)
			}
			if v < 1 || v > 100 {
				return "", 0, fmt.Errorf("count is from 1 to 100")
			}
			n = v
			seenN = true
		}
	}
	if mode == "demo" {
		n = 3
	}
	return mode, n, nil
}

func termWidth(fd int) int {
	w, _, err := term.GetSize(fd)
	if err != nil || w < 20 {
		return 80
	}
	return w
}

func bank(mode string) []prompt {
	var out []prompt
	if mode == "shell" || mode == "mix" {
		for _, s := range shellLines {
			out = append(out, prompt{kind: "shell", text: s})
		}
	}
	if mode == "chat" || mode == "mix" {
		for _, s := range chatLines {
			out = append(out, prompt{kind: "chat", text: s})
		}
	}
	return out
}

func demoPrompts() []prompt {
	return []prompt{
		{kind: "shell", text: `git commit -m "fix the timeout"`},
		{kind: "shell", text: `echo "$PATH" | tr ':' '\n'`},
		{kind: "chat", text: `can you explain this error in plain language?`},
	}
}

func pick(mode string, n, width int) []prompt {
	if mode == "demo" {
		return demoPrompts()
	}
	pool := bank(mode)
	limit := width - 4
	var fit []prompt
	for _, p := range pool {
		if p.text != "" && len([]rune(p.text)) <= limit {
			fit = append(fit, p)
		}
	}
	if len(fit) >= 8 {
		pool = fit
	}
	if len(pool) == 0 || n < 1 {
		return nil
	}
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	out := make([]prompt, 0, n)
	i := 0
	for len(out) < n {
		if i == len(pool) {
			rand.Shuffle(len(pool), func(a, b int) { pool[a], pool[b] = pool[b], pool[a] })
			i = 0
			if pool[0].text == out[len(out)-1].text && len(pool) > 1 {
				pool[0], pool[1] = pool[1], pool[0]
			}
		}
		out = append(out, pool[i])
		i++
	}
	return out
}

// play prints one prompt and reads keys until the line matches, or until stop.
// A finished line returns its stat. A quit returns nil, true.
func play(out io.Writer, next func() (rune, error), p prompt, index, total int) (*stat, bool) {
	fmt.Fprintf(out, "\033[2m  %s  %d/%d\033[0m\r\n\r\n", p.kind, index, total)
	draw(out, p.text, nil, true, false)

	target := []rune(p.text)
	typed := make([]rune, 0, len(target))
	var correct, wrong int
	var started time.Time
	timing := false

	for {
		r, err := next()
		if err != nil || r == keyQuit {
			fmt.Fprint(out, "\r\033[K\r\n")
			return nil, true
		}
		if r == keyIgnore {
			continue
		}
		if r == keyBack {
			if len(typed) > 0 {
				typed = typed[:len(typed)-1]
				draw(out, p.text, typed, true, true)
			}
			continue
		}
		if !unicode.IsPrint(r) {
			continue
		}
		if !timing {
			started = time.Now()
			timing = true
		}
		// A held key can repeat faster than the line can wrap. Count the
		// miss, and stop growing the buffer a few characters past the end.
		if len(typed) >= len(target)+4 {
			wrong++
			continue
		}
		idx := len(typed)
		if idx < len(target) && r == target[idx] {
			correct++
		} else {
			wrong++
		}
		typed = append(typed, r)
		if string(typed) == p.text {
			st := stat{
				runes:   len(target),
				correct: correct,
				wrong:   wrong,
				elapsed: time.Since(started),
			}
			fmt.Fprint(out, "\033[A")
			fmt.Fprintf(out, "\r\033[K  %s\r\n", p.text)
			fmt.Fprintf(out, "\r\033[K\033[2m  %s\033[0m\r\n\r\n", st.String())
			return &st, false
		}
		draw(out, p.text, typed, true, true)
	}
}

// draw paints the reference line and the input line. The matched prefix of
// the reference dims, so the part still to type stays easy to read.
func draw(out io.Writer, target string, typed []rune, cursor bool, up bool) {
	if up {
		fmt.Fprint(out, "\033[A")
	}
	fmt.Fprintf(out, "\r\033[K%s\r\n", paintTarget(target, typed))
	writeInput(out, target, typed, cursor)
}

func paintTarget(target string, typed []rune) string {
	t := []rune(target)
	matched := 0
	for matched < len(typed) && matched < len(t) && typed[matched] == t[matched] {
		matched++
	}
	var b strings.Builder
	b.WriteString("  ")
	if matched > 0 {
		b.WriteString("\033[2m")
		b.WriteString(string(t[:matched]))
		b.WriteString("\033[0m")
	}
	b.WriteString(string(t[matched:]))
	return b.String()
}

func writeInput(out io.Writer, target string, typed []rune, cursor bool) {
	t := []rune(target)
	var b strings.Builder
	bad := false
	for i, ch := range typed {
		match := !bad && i < len(t) && ch == t[i]
		if match {
			b.WriteRune(ch)
			continue
		}
		if !bad {
			b.WriteString("\033[31m")
			bad = true
		}
		b.WriteRune(ch)
	}
	b.WriteString("\033[0m")
	if cursor {
		b.WriteString("\033[7m \033[0m")
	}
	fmt.Fprintf(out, "\r\033[K  %s", b.String())
}

func (s stat) wpm() int {
	secs := s.elapsed.Seconds()
	if secs < 0.001 {
		secs = 0.001
	}
	return int(math.Round((float64(s.runes) / 5) / (secs / 60)))
}

func (s stat) accuracy() int {
	total := s.correct + s.wrong
	if total == 0 {
		return 100
	}
	a := int(math.Round(100 * float64(s.correct) / float64(total)))
	if s.wrong > 0 && a == 100 {
		return 99
	}
	if a < 0 {
		return 0
	}
	if a > 100 {
		return 100
	}
	return a
}

func (s stat) String() string {
	parts := []string{
		fmt.Sprintf("%d wpm", s.wpm()),
		fmt.Sprintf("%d%%", s.accuracy()),
	}
	if s.wrong > 0 {
		parts = append(parts, missesWord(s.wrong))
	}
	parts = append(parts, fmtDur(s.elapsed.Seconds()))
	return strings.Join(parts, " · ")
}

func missesWord(n int) string {
	if n == 1 {
		return "1 miss"
	}
	return fmt.Sprintf("%d misses", n)
}

func linesWord(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func fmtDur(s float64) string {
	if s < 0 {
		s = 0
	}
	if s < 60 {
		return fmt.Sprintf("%.1fs", s)
	}
	sec := int(math.Round(s))
	return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
}

func combine(all []stat) stat {
	var s stat
	for _, x := range all {
		s.runes += x.runes
		s.correct += x.correct
		s.wrong += x.wrong
		s.elapsed += x.elapsed
	}
	return s
}

func sessionLine(all []stat) string {
	if len(all) == 0 {
		return "  no finished lines"
	}
	return "  " + linesWord(len(all)) + " · " + combine(all).String()
}

// Raw mode delivers Ctrl-C as a byte instead of SIGINT.
func readKey() (rune, error) {
	b, err := readByte()
	if err != nil {
		return 0, err
	}
	switch b {
	case 0x03, 0x04: // ctrl-c, ctrl-d
		return keyQuit, nil
	case 0x7f, 0x08: // backspace
		return keyBack, nil
	case '\r', '\n':
		return keyIgnore, nil
	case 0x1b:
		return readEscape()
	}
	if b < 0x20 {
		return keyIgnore, nil
	}
	if b < 0x80 {
		return rune(b), nil
	}
	return decodeUTF8(b)
}

func readEscape() (rune, error) {
	ready, err := waitInput(50 * time.Millisecond)
	if err != nil {
		return 0, err
	}
	if !ready {
		return keyQuit, nil
	}
	next, err := readByte()
	if err != nil {
		return 0, err
	}
	if next != '[' && next != 'O' {
		return keyIgnore, nil
	}
	// Swallow the rest of an arrow or function key.
	for i := 0; i < 8; i++ {
		c, err := readByte()
		if err != nil {
			return 0, err
		}
		if next == 'O' || (c >= 0x40 && c <= 0x7e) {
			break
		}
	}
	return keyIgnore, nil
}

func waitInput(timeout time.Duration) (bool, error) {
	fd := int(os.Stdin.Fd())
	var set unix.FdSet
	set.Set(fd)
	tv := unix.NsecToTimeval(timeout.Nanoseconds())
	for {
		n, err := unix.Select(fd+1, &set, nil, nil, &tv)
		if errors.Is(err, unix.EINTR) {
			set = unix.FdSet{}
			set.Set(fd)
			tv = unix.NsecToTimeval(timeout.Nanoseconds())
			continue
		}
		if err != nil {
			return false, err
		}
		return n > 0, nil
	}
}

func readByte() (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(os.Stdin, b[:])
	return b[0], err
}

func decodeUTF8(first byte) (rune, error) {
	n := 0
	switch {
	case first&0xe0 == 0xc0:
		n = 2
	case first&0xf0 == 0xe0:
		n = 3
	case first&0xf8 == 0xf0:
		n = 4
	default:
		return keyIgnore, nil
	}
	buf := make([]byte, n)
	buf[0] = first
	if _, err := io.ReadFull(os.Stdin, buf[1:]); err != nil {
		return 0, err
	}
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError || size != n {
		return keyIgnore, nil
	}
	return r, nil
}
