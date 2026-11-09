// llmnrf scans local listening TCP ports, probes them for LLM provider
// endpoints (OpenAI-compatible, Anthropic-compatible, llama.cpp, Ollama,
// LM Studio, vLLM), maps ports to owning processes, and renders the results
// in a Bubble Tea dashboard.
//
// Usage:
//
//	llmnrf            interactive TUI
//	llmnrf -plain     one-shot text report (no TUI), for scripts/CI
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"

	"llmnrf/internal/discovery"
	"llmnrf/internal/probe"
	"llmnrf/internal/ui"
)

func main() {
	plain := flag.Bool("plain", false, "print a text report instead of the TUI")
	flag.Parse()

	if *plain {
		report()
		return
	}

	p := tea.NewProgram(ui.New(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "llmnrf:", err)
		os.Exit(1)
	}
}

func report() {
	ls, err := discovery.Listeners()
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery:", err)
		os.Exit(1)
	}
	owners, err := discovery.Owners()
	if err != nil {
		fmt.Fprintln(os.Stderr, "process map:", err)
		os.Exit(1)
	}

	type line struct {
		port   int
		addr   string
		res    probe.Result
		isLLM  bool
		proc   string
	}
	var lines []line
	for _, l := range ls {
		res := probe.Probe(probe.Target{Addr: l.Addr(), Port: l.Port})
		proc := "-"
		if p, ok := owners[l.Inode]; ok {
			proc = fmt.Sprintf("#%d %s (%s)", p.PID, p.Name, p.Mem())
		}
		lines = append(lines, line{port: l.Port, addr: l.Addr(), res: res, isLLM: res.IsLLM, proc: proc})
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].isLLM != lines[j].isLLM {
			return lines[i].isLLM
		}
		return lines[i].port < lines[j].port
	})

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ENDPOINT\tENGINE\tSTATUS\tMODEL\tCTX\tQUANT\tPROCESS/RSS")
	for _, ln := range lines {
		model, ctx, quant := "-", "-", "-"
		engine := "-"
		status := "listener"
		if ln.isLLM {
			engine = ln.res.EngineSummary()
			status = "running"
			if len(ln.res.Models) > 0 {
				model = ln.res.Models[0]
				if len(ln.res.Models) > 1 {
					model += fmt.Sprintf(" +%d", len(ln.res.Models)-1)
				}
			}
			if ln.res.Context > 0 {
				ctx = fmt.Sprint(ln.res.Context)
			}
			if ln.res.Quant != "" {
				quant = ln.res.Quant
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			ln.addr, engine, status, model, ctx, quant, ln.proc)
	}
	w.Flush()

	for _, ln := range lines {
		if !ln.isLLM {
			continue
		}
		fmt.Printf("\n=== %s (%s) ===\n", ln.addr, ln.res.EngineSummary())
		for _, ep := range ln.res.Endpoints {
			fmt.Printf("-- %s %s (HTTP %d)\n%s\n", ep.Name, ep.Path, ep.Status, ep.Body)
		}
	}
}
