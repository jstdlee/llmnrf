<div align="center">

# llmnrf

**Point it at localhost. It tells you which AI servers you're actually running.**

Every listening port on your machine, probed against the endpoint surface of
the major local LLM runtimes — rendered in a live terminal dashboard.

`Go` · `Bubble Tea` · `Linux` — zero config, zero network calls

</div>

---

## What it does

llmnrf reads your kernel's listening-socket table (`/proc/net/tcp`), so port
discovery is instant and exhaustive — no blind scanning. Each listener is then
probed with short-timeout requests against the endpoint patterns of the
common AI server runtimes:

| Pattern | Endpoints | Identifies |
|---|---|---|
| **OpenAI-compatible** | `GET /v1/models` | ollama, vLLM, llama.cpp, LM Studio, LiteLLM, … |
| **Anthropic-compatible** | `POST /v1/messages` error-envelope, versioned `GET /v1/models` | anthropic-spec routers & proxies |
| **llama.cpp server** | `GET /health`, `GET /props` | model path, context (`n_ctx`) |
| **Ollama** | `GET /api/tags`, `POST /api/show` | models, params, quantization, context length |
| **LM Studio** | `GET /api/v0/models` | quantization, max context, state |
| **vLLM / SGLang** | `GET /version`, `GET /get_model_info` | engine version, model path |
| **ComfyUI** | `GET /system_stats`, `GET /queue` | version, OS, Python, GPU + VRAM |
| **Generic health** | `GET /health`, `GET /healthz` | liveness only |

For every confirmed provider it extracts as much as the JSON gives up:
engine + version, model names, context window, quantization level,
parameter size, architecture — and maps the port to its owning process with
PID and resident memory.

## Dashboard

```text
llmnrf — local LLM endpoint scanner

   ENDPOINT                  ENGINE                     STATUS     MODEL                        CTX     QUANT    PROCESS / RSS
 > 127.0.0.1:28080           llama.cpp server [openai]  ● running  mistral-7b-instruct-v0.3.Q5  32768   -        #4821 llama-server 2.4 GiB
   127.0.0.1:21434           Ollama [openai+anthropic]  ● running  qwen3:8b                     -       Q4_K_M   #4812 ollama 21.3 MiB
   127.0.0.1:21888           ComfyUI 0.3.45             ● running  -                            -       -        #4903 python3 512.7 MiB
   127.0.0.1:29999           -                          listener   -                            -       -        #4755 python3 12.1 MiB
   127.0.0.1:5432            -                          listener   -                            -       -        #1902 postgres 143.8 MiB

 3 LLM provider(s), 2 other listener(s) · enter=raw JSON · r=rescan · q=quit
```

## Usage

```sh
llmnrf            # interactive TUI
llmnrf -plain     # one-shot text report — great for scripts and CI
```

Keys in the TUI:

| Key | Action |
|---|---|
| `j` / `k`, arrows | move selection |
| `enter` | raw JSON of every probed endpoint on the selected row |
| `r` | rescan |
| `esc` | back |
| `q` | quit |

Rows that can't be parsed still land in the raw-JSON view (pretty-printed when
valid JSON), and each port's combined capture is written to
`$TMPDIR/llmnrf-<port>.json`, linked inline with an OSC-8 terminal hyperlink.

## Privacy

- **Local only.** Everything llmnrf does is on your machine: it reads `/proc`
  and talks to `127.0.0.1`-bound listeners. There is no telemetry, no
  outbound traffic, no analytics.
- **Read-only against your servers.** Probes are unauthenticated `GET`s plus
  two empty-body `POST`s (`/api/show`, `/v1/messages`) that fail fast without
  API keys and never generate completions.
- **Nothing leaves the terminal.** Results are rendered, optionally saved to
  your own temp dir, and never uploaded.
- **Careful what you share.** The dashboard shows process names and memory
  use; don't paste screenshots into public issues without checking them.

## Building

```sh
go build -o llmnrf .
```

Requirements: Go 1.25+, Linux (the discovery layer reads `/proc`).

## Project layout

```
main.go                  entry point; TUI or -plain report
internal/discovery/      /proc listener enumeration, socket-inode → PID + RSS
internal/probe/          endpoint probing and response parsing
internal/ui/             Bubble Tea model: progress, dashboard, detail viewport
```

## License

[MIT](LICENSE)
