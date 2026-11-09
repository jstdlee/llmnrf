// Package ui renders the llmnrf Bubble Tea interface: live probe progress
// followed by a dashboard of discovered LLM providers and their processes.
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"llmnrf/internal/discovery"
	"llmnrf/internal/probe"
)

// ProbeMsg carries one finished probe result into the model.
type ProbeMsg struct {
	Target probe.Target
	Result probe.Result
}

type scanDoneMsg struct{}
type ownersMsg struct {
	owners map[string]discovery.Process
	err    error
}

// Model is the top-level Bubble Tea model.
type Model struct {
	listeners []discovery.Listener
	targets   []probe.Target
	results   map[string]probe.Result // key: addr
	owners    map[string]discovery.Process
	pending   int
	spinner   spinner.Model
	err       error

	sel    int
	detail *viewport.Model
	width  int
	height int
}

// New builds the model and kicks off discovery + probing.
func New() Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	m := Model{spinner: s, results: map[string]probe.Result{}}
	m.refresh()
	return m
}

func (m *Model) refresh() {
	m.results = map[string]probe.Result{}
	m.sel = 0
	m.detail = nil
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
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, loadOwners}
	for _, t := range m.targets {
		t := t
		cmds = append(cmds, func() tea.Msg {
			return ProbeMsg{Target: t, Result: probe.Probe(t)}
		})
	}
	return tea.Batch(cmds...)
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
			m.detail.Height = m.height - 4
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.refresh()
			return m, m.Init()
		case "esc":
			m.detail = nil
			return m, nil
		case "up", "k":
			if m.detail == nil && m.sel > 0 {
				m.sel--
			} else if m.detail != nil {
				m.detail.LineUp(3)
			}
			return m, nil
		case "down", "j":
			if m.detail == nil && m.sel < len(m.rows())-1 {
				m.sel++
			} else if m.detail != nil {
				m.detail.LineDown(3)
			}
			return m, nil
		case "enter":
			if m.detail == nil {
				rows := m.rows()
				if m.sel < len(rows) && rows[m.sel].result != nil {
					vp := viewport.New(m.width, m.height-4)
					vp.SetContent(detailText(rows[m.sel]))
					m.detail = &vp
				}
			}
			return m, nil
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case ProbeMsg:
		m.results[msg.Target.Addr] = msg.Result
		m.pending--
		return m, nil

	case ownersMsg:
		m.owners = msg.owners
		return m, nil
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	var b strings.Builder
	title := titleStyle.Render("llmnrf — local LLM endpoint scanner")
	b.WriteString(title + "\n\n")

	if m.err != nil {
		b.WriteString(errStyle.Render("discovery error: " + m.err.Error()) + "\n")
		return b.String()
	}

	if m.detail != nil {
		return m.detailView()
	}

	done := len(m.results)
	if m.pending > 0 {
		b.WriteString(fmt.Sprintf("%s probing %d listeners — %d done\n\n",
			m.spinner.View(), m.pending, done))
		for _, t := range m.targets {
			status := "…"
			if r, ok := m.results[t.Addr]; ok {
				status = statusFor(r)
			}
			line := fmt.Sprintf("  %-24s %s", t.Addr, status)
			if strings.Contains(status, "● running") {
				line = okStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
		return b.String()
	}

	rows := m.rows()
	llm, other := 0, 0
	for _, r := range rows {
		if r.isLLM {
			llm++
		} else {
			other++
		}
	}

	b.WriteString(headerStyle.Render(fmt.Sprintf(" %-3s %-24s %-20s %-8s %-26s %-8s %-6s %-22s",
		"", "ENDPOINT", "ENGINE", "STATUS", "MODEL", "CTX", "QUANT", "PROCESS / RSS")))
	for i, r := range rows {
		cursor := " "
		st := rowStyle
		if i == m.sel {
			cursor = ">"
			st = selStyle
		}
		b.WriteString(st.Render(r.render(cursor)) + "\n")
	}
	b.WriteString(fmt.Sprintf("\n%s\n", dimStyle.Render(
		fmt.Sprintf("%d LLM provider(s), %d other listener(s) · enter=raw JSON · r=rescan · q=quit", llm, other))))
	return b.String()
}

func (m Model) detailView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("llmnrf — raw endpoint JSON (esc=back)") + "\n\n")
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

func (r row) render(cursor string) string {
	proc := "-"
	mem := ""
	if r.proc != nil {
		proc = fmt.Sprintf("#%d %s", r.proc.PID, r.proc.Name)
		mem = r.proc.Mem()
	}
	if !r.isLLM {
		return fmt.Sprintf("%-3s %-24s %-20s %-8s %-26s %-8s %-6s %-22s",
			cursor, r.addr, "-", "listener", "-", "-", "-", proc+" "+mem)
	}
	res := r.result
	model := "-"
	if len(res.Models) > 0 {
		model = truncate(res.Models[0], 25)
		if len(res.Models) > 1 {
			model += fmt.Sprintf(" +%d", len(res.Models)-1)
		}
	}
	ctx := "-"
	if res.Context > 0 {
		ctx = strconv.Itoa(res.Context)
	}
	return fmt.Sprintf("%-3s %-24s %-20s %-8s %-26s %-8s %-6s %-22s",
		cursor, r.addr, truncate(res.EngineSummary(), 19), "● running",
		model, ctx, orDash(res.Quant), proc+" "+mem)
}

func statusFor(r probe.Result) string {
	if r.IsLLM {
		return "● running " + r.EngineSummary()
	}
	return "· no LLM endpoints"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return truncate(s, 5)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
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

// detailText renders the raw JSON bodies of one row and also writes a
// combined file, returning the text plus a clickable OSC-8 link.
func detailText(r row) string {
	var b strings.Builder
	if r.result == nil {
		return "no probe data"
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
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	rowStyle    = lipgloss.NewStyle()
	selStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)
