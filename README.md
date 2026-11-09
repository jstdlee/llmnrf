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

For every provider it extracts as much as the JSON gives up: engine + version,
model names, context window, quantization level, parameter size, architecture —
and maps the port to its owning process with PID, resident memory and the
executable path (`/proc/<pid>/exe`).

An engine **name** is only printed when the response has a shape that product
emits. Shared surfaces (`/v1/models`, `/health`, `/version`, an
`/api/tags`-shaped reply) identify an API, not a product, so those rows read
`unknown` and the reason is recorded as evidence — `d` shows the tier
(`confirmed` / `probable`), the evidence, and the API surfaces that answered.

## Dashboard

```text
llmnrf — local LLM endpoint scanner

   ENDPOINT            ENGINE                        CONF       STATUS     MODEL                       CTX     QUANT  PROCESS / RSS
 > 127.0.0.1:28080     llama.cpp server [openai]     confirmed  ● running  mistral-7b-instruct-v0.3.Q5 32768   -      #4821 llama-server 2.4 GiB
   127.0.0.1:21434     Ollama 0.33.1 [openai]        confirmed  ● running  qwen3:8b                    -       Q4_K_M #4812 ollama 21.3 MiB
   127.0.0.1:21888     ComfyUI 0.3.45                confirmed  ● running  -                           -       -      #4903 python3 512.7 MiB
   127.0.0.1:21435     unknown [openai+anthropic]    -          ● running  someproxy:latest            -       -      #4812 someproxy 21.3 MiB
   127.0.0.1:5432      -                             -          listener   -                           -       -      #1902 postgres 143.8 MiB

 3 LLM provider(s), 2 other listener(s) · 5/5 shown · j/k move · d=detail · enter=raw JSON · r=rescan · q=quit
 scanned 14:02:11 · next refresh in 22s
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
| `d` | detail: identity, confidence, evidence, owning process and app path, then the raw JSON |
| `enter` | raw JSON of every probed endpoint on the selected row |
| `r` | rescan now |
| `esc` | back |
| `q` | quit |

Rows that can't be parsed still land in the raw-JSON view (pretty-printed when
valid JSON), and each port's combined capture is written to
`$TMPDIR/llmnrf-<port>.json`, linked inline with an OSC-8 terminal hyperlink.

## Refresh and sizing

The dashboard re-scans every 30 seconds on its own; the footer shows when the
last scan finished and when the next one runs. A background refresh keeps the
previous results on screen until the new ones arrive, and the cursor stays on
the same endpoint. Columns shrink to fit the terminal (nothing wraps), and the
list scrolls to keep the selection in view. `AutoRefresh` is one constant in
`internal/ui/ui.go`.

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
