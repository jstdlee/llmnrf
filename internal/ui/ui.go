// Package ui renders the llmnrf Bubble Tea interface: live probe progress
// followed by a dashboard of discovered LLM providers and their processes.
// The dashboard re-scans on a timer and lays its columns out to fit the
// terminal.
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"llmnrf/internal/discovery"
	"llmnrf/internal/probe"
)

// AutoRefresh is how long a scan stays on screen before every listener is
// probed again.
const AutoRefresh = 30 * time.Second

// Detail view modes.
const (
	viewDetail = iota // summary + raw JSON
	viewRaw           // raw JSON only
)

// ProbeMsg carries one finished probe result into the model.
type ProbeMsg struct {
	Target probe.Target
	Result probe.Result
}

type ownersMsg struct {
	owners map[string]discovery.Process
	err    error
}

// autoRefreshMsg fires when the previous scan has aged out.
type autoRefreshMsg struct{ at time.Time }

// Model is the top-level Bubble Tea model.
type Model struct {
	listeners []discovery.Listener
	targets   []probe.Target
	results   map[string]probe.Result // key: addr
	owners    map[string]discovery.Process
	pending   int
	scanning  bool
	lastScan  time.Time
	spinner   spinner.Model
	err       error

	sel        int
	selAddr    string // keeps the cursor on the same endpoint across refreshes
	detail     *viewport.Model
	detailMode int
	width      int
	height     int
}

// New builds the model and kicks off discovery + probing.
func New() Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	m := Model{spinner: s, results: map[string]probe.Result{}}
	m.refresh(false, false)
	return m
}

// refresh re-enumerates listeners and re-probes. keepSel holds the cursor on
// the same endpoint; keepResults (the background timer) keeps the previous
// scan on screen and leaves the open detail view alone, so a refresh never
// blanks the dashboard.
func (m *Model) refresh(keepSel, keepResults bool) {
	if !keepResults {
		m.results = map[string]probe.Result{}
		m.detail = nil
	}
	ls, err := discovery.Listeners()
	if err != nil {
		m.err = err
		return
	}
	m.listeners = ls
	m.targets = make([]probe.Target, 0, len(ls))
	for _, l := range ls {
		m.targets = append(m.targets, probe.Target{Addr: l.Addr(), Port: l.Port})
	}
	m.pending = len(m.targets)
	m.scanning = true
	m.lastScan = time.Now()

	m.sel = 0
	if keepSel && m.selAddr != "" {
		for i, t := range m.targets {
			if t.Addr == m.selAddr {
				m.sel = i
				break
			}
		}
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, loadOwners, m.scan(), scheduleRefresh())
}

// scan fans out one probe command per listener.
func (m Model) scan() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.targets))
	for _, t := range m.targets {
		t := t
		cmds = append(cmds, func() tea.Msg {
			return ProbeMsg{Target: t, Result: probe.Probe(t)}
		})
	}
	return tea.Batch(cmds...)
}

func scheduleRefresh() tea.Cmd {
	return tea.Tick(AutoRefresh, func(t time.Time) tea.Msg { return autoRefreshMsg{at: t} })
}

func loadOwners() tea.Msg {
	owners, err := discovery.Owners()
	return ownersMsg{owners: owners, err: err}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.detail != nil {
			m.detail.Width = m.width
			m.detail.Height = m.height - 3
		}
		return m, nil

	case autoRefreshMsg:
		if m.scanning {
			// A scan is still running; try again on the next tick.
			return m, scheduleRefresh()
		}
		m.refresh(true, true)
		return m, tea.Batch(m.scan(), scheduleRefresh())

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.refresh(false, false)
			return m, tea.Batch(m.scan(), scheduleRefresh())
		case "esc":
			m.detail = nil
			return m, nil
		case "up", "k":
			if m.detail != nil {
				m.detail.LineUp(3)
				return m, nil
			}
			if m.sel > 0 {
				m.sel--
				m.selAddr = m.rows()[m.sel].addr
			}
			return m, nil
		case "down", "j":
			if m.detail != nil {
				m.detail.LineDown(3)
				return m, nil
			}
			if rows := m.rows(); m.sel < len(rows)-1 {
				m.sel++
				m.selAddr = rows[m.sel].addr
			}
			return m, nil
		case "d":
			return m, m.openDetail(viewDetail)
		case "enter":
			return m, m.openDetail(viewRaw)
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case ProbeMsg:
		m.results[msg.Target.Addr] = msg.Result
		m.pending--
		if m.pending <= 0 {
			m.pending = 0
			m.scanning = false
			m.lastScan = time.Now()
		}
		return m, nil

	case ownersMsg:
		m.owners = msg.owners
		return m, nil
	}
	return m, nil
}

// openDetail builds the detail viewport for the selected row.
func (m *Model) openDetail(mode int) tea.Cmd {
	rows := m.rows()
	if m.sel >= len(rows) {
		return nil
	}
	r := rows[m.sel]
	if mode == viewRaw && r.result == nil {
		return nil
	}
	vp := viewport.New(m.width, m.height-3)
	vp.SetContent(detailText(r, mode))
	m.detail = &vp
	m.detailMode = mode
	return nil
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	if m.detail != nil {
		return m.detailView()
	}
	if m.err != nil {
		return titleStyle.Render("llmnrf — local LLM endpoint scanner") + "\n\n" +
			errStyle.Render("discovery error: "+m.err.Error()) + "\n"
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("llmnrf — local LLM endpoint scanner") + "\n\n")

	if m.scanning {
		b.WriteString(fmt.Sprintf("%s probing %d listeners — %d done\n\n",
			m.spinner.View(), m.pending, len(m.targets)-m.pending))
	}

	w := m.widths()
	rows := m.rows()

	if m.scanning && len(rows) == 0 {
		return b.String()
	}

	b.WriteString(headerStyle.Render(header(w)) + "\n")

	// Fit the list to the window: title + blank + status + header + blank +
	// help line.
	listH := m.height - 6
	if m.scanning {
		listH -= 2
	}
	if listH < 1 {
		listH = 1
	}
	top := 0
	if m.sel >= listH {
		top = m.sel - listH + 1
	}
	if top > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d above", top)) + "\n")
	}
	for i := top; i < len(rows) && i < top+listH; i++ {
		st := rowStyle
		cur := strconv.Itoa(i + 1)
		if i == m.sel {
			st = selStyle
			cur = ">"
		}
		b.WriteString(st.Render(rows[i].render(cur, w)) + "\n")
	}
	if len(rows) > top+listH {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d below", len(rows)-top-listH)) + "\n")
	}

	llm, other := 0, 0
	for _, r := range rows {
		if r.isLLM {
			llm++
		} else {
			other++
		}
	}
	shown := listH
	if shown > len(rows) {
		shown = len(rows)
	}
	b.WriteString("\n" + dimStyle.Render(fmt.Sprintf(
		"%d LLM provider(s), %d other listener(s) · %d/%d shown · j/k move · d=detail · enter=raw JSON · r=rescan · q=quit",
		llm, other, shown, len(rows))) + "\n")
	b.WriteString(dimStyle.Render(m.refreshLine()) + "\n")
	return b.String()
}

// refreshLine reports when the last scan finished and when the next one runs.
func (m Model) refreshLine() string {
	if m.scanning {
		return "scanning now · auto-refresh every " + shortDur(AutoRefresh)
	}
	left := time.Until(m.lastScan.Add(AutoRefresh))
	if left < 0 {
		left = 0
	}
	return fmt.Sprintf("scanned %s · next refresh in %s",
		m.lastScan.Format("15:04:05"), shortDur(left.Round(time.Second)))
}

func shortDur(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func (m Model) detailView() string {
	var b strings.Builder
	head := "detail"
	if m.detailMode == viewRaw {
		head = "raw endpoint JSON"
	}
	b.WriteString(titleStyle.Render("llmnrf — "+head+" (esc=back)") + "\n\n")
	b.WriteString(m.detail.View())
	return b.String()
}

type row struct {
	addr     string
	isLLM    bool
	result   *probe.Result
	listener discovery.Listener
	proc     *discovery.Process
}

// header renders the column titles at the given widths.
func header(w []int) string {
	cols := []string{"", "ENDPOINT", "ENGINE", "STATUS", "MODEL", "CTX", "QUANT", "PROCESS / RSS"}
	var b strings.Builder
	b.WriteString("   ")
	for i, c := range cols[1:] {
		b.WriteString(pad(c, w[i]) + " ")
	}
	return b.String()
}

func (r row) render(cursor string, w []int) string {
	proc := "-"
	mem := ""
	if r.proc != nil {
		proc = fmt.Sprintf("#%d %s", r.proc.PID, r.proc.Name)
		mem = r.proc.Mem()
	}
	cells := []string{cursor, r.addr, "-", "listener", "-", "-", "-", proc + " " + mem}
	if r.isLLM {
		res := r.result
		model := "-"
		if len(res.Models) > 0 {
			model = res.Models[0]
			if len(res.Models) > 1 {
				model += fmt.Sprintf(" +%d", len(res.Models)-1)
			}
		}
		ctx := "-"
		if res.Context > 0 {
			ctx = strconv.Itoa(res.Context)
		}
		cells = []string{cursor, r.addr, res.EngineSummary(), "● running",
			model, ctx, orDash(res.Quant), proc + " " + mem}
	}
	var b strings.Builder
	for i, c := range cells {
		if i == 0 {
			b.WriteString(" " + c + " ")
			continue
		}
		b.WriteString(pad(c, w[i-1]) + " ")
	}
	return b.String()
}

// widths returns one width per column. Preferred widths are used when the
// terminal is wide enough; otherwise the slack above each column's minimum is
// shared out proportionally, so nothing ever wraps or overflows.
func (m Model) widths() []int {
	pref := []int{24, 20, 12, 26, 8, 6, 22}
	min := []int{14, 10, 10, 10, 5, 5, 12}
	// One cell for the cursor plus one space after every column.
	const gutter = 10
	avail := m.width - gutter
	if avail <= 0 {
		return []int{0, 0, 0, 0, 0, 0, 0}
	}
	want, slack, minSum := 0, 0, 0
	for i := range pref {
		want += pref[i]
		minSum += min[i]
		slack += pref[i] - min[i]
	}
	if avail >= want || slack <= 0 {
		return pref
	}
	if avail < minSum {
		// Narrower than the minimums: share what is there out proportionally.
		w := make([]int, len(min))
		for i := range w {
			w[i] = avail * min[i] / minSum
		}
		return w
	}
	w := append([]int(nil), min...)
	extra := avail - minSum
	used := 0
	for i := range w {
		share := extra * (pref[i] - min[i]) / slack
		w[i] += share
		used += share
	}
	for i := 0; used < extra; i = (i + 1) % len(w) {
		if pref[i] > min[i] {
			w[i]++
			used++
		}
	}
	return w
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return truncate(s, 5)
}

// pad truncates s to n display cells and left-pads it with spaces.
func pad(s string, n int) string {
	if n <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w > n {
		return truncate(s, n)
	}
	return s + strings.Repeat(" ", n-w)
}

// truncate cuts s to n display cells, so wide glyphs never overflow a column.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	total := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if total+rw > n-1 {
			return s[:i] + "…"
		}
		total += rw
	}
	return s
}

func (m Model) rows() []row {
	rows := make([]row, 0, len(m.listeners))
	for _, l := range m.listeners {
		r := row{addr: l.Addr(), listener: l}
		if res, ok := m.results[r.addr]; ok && res.IsLLM {
			r.isLLM = true
			res := res
			r.result = &res
		}
		if m.owners != nil {
			if p, ok := m.owners[l.Inode]; ok {
				p := p
				r.proc = &p
			}
		}
		rows = append(rows, r)
	}
	// LLM providers first, then other listeners.
	sortRows(rows)
	return rows
}

func sortRows(rows []row) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].isLLM && !rows[j-1].isLLM; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// detailText renders one row: a summary block (identity, confidence, evidence,
// owning process and its executable path) followed by the raw JSON bodies. It
// also writes the combined capture to the row's temp file.
func detailText(r row, mode int) string {
	var b strings.Builder
	if mode == viewDetail {
		b.WriteString(summaryText(r))
		b.WriteString("\n")
	}
	if r.result == nil {
		if mode == viewRaw {
			return "no probe data"
		}
		return b.String()
	}
	path := rawFilePath(r.listener.Port)
	var combined []byte
	for _, ep := range r.result.Endpoints {
		pretty := prettyJSON(ep.Body)
		b.WriteString(dimStyle.Render(fmt.Sprintf("── %s %s (HTTP %d) ──\n", ep.Name, ep.Path, ep.Status)))
		b.WriteString(pretty + "\n\n")
		combined = append(combined, []byte(fmt.Sprintf("// %s %s (HTTP %d)\n", ep.Name, ep.Path, ep.Status))...)
		combined = append(combined, []byte(pretty+"\n\n")...)
	}
	if link, err := hyperlink("file://"+path, path); err == nil {
		b.WriteString("\nfull raw JSON: " + link + "\n")
	}
	_ = os.WriteFile(path, combined, 0o600)
	return b.String()
}

// summaryText is the "what are we actually looking at" block: identity with
// its confidence, the evidence behind it, and the process behind the socket.
func summaryText(r row) string {
	var b strings.Builder
	line := func(k, v string) {
		if v == "" {
			v = "-"
		}
		b.WriteString(fmt.Sprintf("%-12s %s\n", k, v))
	}
	line("endpoint", r.addr)
	if res := r.result; res != nil {
		line("engine", res.EngineSummary())
		line("confidence", res.Confidence)
		if len(res.Surface) > 0 {
			line("api surface", strings.Join(res.Surface, ", "))
		}
		if len(res.Evidence) > 0 {
			line("evidence", strings.Join(res.Evidence, "; "))
		}
		if len(res.Models) > 0 {
			line("models", strings.Join(res.Models, ", "))
		}
		if res.Context > 0 {
			line("context", strconv.Itoa(res.Context))
		}
		line("quant", res.Quant)
		line("params", res.Params)
		line("arch", res.Arch)
		line("vendor", res.Vendor)
		for _, kv := range res.Extra {
			line("· "+kv.K, kv.V)
		}
	} else {
		line("status", "listener, no LLM endpoints")
	}
	if p := r.proc; p != nil {
		line("process", fmt.Sprintf("#%d %s (%s)", p.PID, p.Name, p.Mem()))
		line("app path", p.Exe)
		if p.Cmdline != "" && p.Cmdline != p.Name {
			line("cmdline", truncateLine(p.Cmdline, 200))
		}
	}
	return b.String()
}

func truncateLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func rawFilePath(port int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("llmnrf-%d.json", port))
}

func prettyJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s // not JSON: show raw body verbatim
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s
	}
	return string(out)
}

// hyperlink wraps text in an OSC-8 terminal hyperlink.
func hyperlink(url, text string) (string, error) {
	if strings.ContainsRune(text, '\x1b') {
		return "", fmt.Errorf("unsafe link text")
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\", nil
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	headerStyle = lipgloss.NewStyle().Bold(true).Faint(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	rowStyle    = lipgloss.NewStyle()
	selStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)
