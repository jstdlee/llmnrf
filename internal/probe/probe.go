// Package probe classifies listening ports as LLM provider endpoints by
// probing the common health / model-list surfaces of OpenAI-compatible,
// Anthropic-compatible, llama.cpp, Ollama, LM Studio, vLLM and ComfyUI servers.
//
// A product name is only ever attached to a port when the response has a shape
// that product emits. Shared surfaces (/v1/models, /health, /api/tags-shaped
// responses, /version) identify an API, not a product, so they leave the engine
// unknown and are recorded as evidence instead.
package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Endpoint is one probed HTTP surface on a port.
type Endpoint struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Status int    `json:"status,omitempty"`
	Err    string `json:"error,omitempty"`
	Body   string `json:"body,omitempty"`
}

// Candidate is one engine identification, with the tier of evidence behind it.
type Candidate struct {
	Engine string `json:"engine"`
	Tier   string `json:"tier"`
	Kind   string `json:"kind"` // payload | header
	Why    string `json:"why"`
}

// Evidence tiers, strongest first.
const (
	TierConfirmed = "confirmed" // a payload shape only this product emits
	TierProbable  = "probable"  // a shape or header shared with other products
)

// Result is everything discovered for one listening port.
type Result struct {
	Addr            string `json:"addr"`
	Port            int    `json:"port"`
	IsLLM           bool   `json:"is_llm"`
	Engine          string `json:"engine,omitempty"`
	Version         string `json:"version,omitempty"`
	Models          []string
	Context         int
	Quant           string
	Params          string
	Arch            string
	Extra           []KV
	Vendor          string      `json:"vendor,omitempty"`
	Confidence      string      `json:"confidence,omitempty"`
	Surface         []string    `json:"surface,omitempty"`
	Evidence        []string    `json:"evidence,omitempty"`
	Candidates      []Candidate `json:"candidates,omitempty"`
	OpenAICompat    bool
	AnthropicCompat bool
	Endpoints       []Endpoint
}

// KV is a parsed key/value detail shown on the dashboard.
type KV struct{ K, V string }

// Target is a listener to probe.
type Target struct {
	Addr string // host:port, pre-joined
	Port int
}

var client = &http.Client{
	Timeout: 2 * time.Second,
	Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext:       (&net.Dialer{Timeout: 1 * time.Second}).DialContext,
	},
}

// surface records an API surface that answered, independent of who serves it.
func (res *Result) surface(name string) {
	if !contains(res.Surface, name) {
		res.Surface = append(res.Surface, name)
	}
}

// consider records a possible engine identity. Nothing is decided here;
// classify picks the strongest candidate at the end.
func (res *Result) consider(engine, tier, kind, why string) {
	if engine == "" {
		return
	}
	for i := range res.Candidates {
		if res.Candidates[i].Engine != engine {
			continue
		}
		if rank(res.Candidates[i]) < rank(Candidate{Tier: tier, Kind: kind}) {
			res.Candidates[i].Tier = tier
			res.Candidates[i].Kind = kind
		}
		if !strings.Contains(res.Candidates[i].Why, why) {
			res.Candidates[i].Why += "; " + why
		}
		return
	}
	res.Candidates = append(res.Candidates, Candidate{Engine: engine, Tier: tier, Kind: kind, Why: why})
}

// rank orders candidates: confirmed payload shape > confirmed other >
// probable payload > probable header.
func rank(c Candidate) int {
	switch {
	case c.Tier == TierConfirmed && c.Kind == "payload":
		return 4
	case c.Tier == TierConfirmed:
		return 3
	case c.Tier == TierProbable && c.Kind == "payload":
		return 2
	default:
		return 1
	}
}

// Probe runs every endpoint check against one target.
func Probe(t Target) Result {
	res := Result{Addr: t.Addr, Port: t.Port}
	base := "http://" + t.Addr

	get := func(name, path string, hdr map[string]string) Endpoint {
		ep := Endpoint{Name: name, Path: path}
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			ep.Err = err.Error()
			return ep
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			ep.Err = err.Error()
			return ep
		}
		defer resp.Body.Close()
		ep.Status = resp.StatusCode
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		ep.Body = strings.TrimSpace(string(body))
		collectHints(&res, resp.Header)
		return ep
	}
	keep := func(ep Endpoint) bool { return ep.Status > 0 }

	eps := []Endpoint{
		get("health", "/health", nil),
		get("healthz", "/healthz", nil),
		get("openai-models", "/v1/models", nil),
		get("anthropic-models", "/v1/models", map[string]string{
			"x-anthropic-version": "2023-06-01", "anthropic-version": "2023-06-01"}),
		get("ollama-tags", "/api/tags", nil),
		get("ollama-version", "/api/version", nil),
		get("lmstudio-models", "/api/v0/models", nil),
		get("llamacpp-props", "/props", nil),
		get("vllm-version", "/version", nil),
		get("sglang-model", "/get_model_info", nil),
		get("comfyui-stats", "/system_stats", nil),
		get("comfyui-queue", "/queue", nil),
	}

	// Anthropic-compatible detection: a POST to /v1/messages that answers with
	// the Anthropic error envelope {"type":"error",...} (400/401, no API key).
	anthReq, err := http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader([]byte(`{}`)))
	if err == nil {
		anthReq.Header.Set("content-type", "application/json")
		anthReq.Header.Set("anthropic-version", "2023-06-01")
		anthReq.Header.Set("x-api-key", "none")
		if resp, err := client.Do(anthReq); err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			resp.Body.Close()
			ep := Endpoint{Name: "anthropic-messages", Path: "/v1/messages", Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
			if isAnthropicErrorShape(body) {
				res.AnthropicCompat = true
				res.surface("anthropic")
			}
			eps = append(eps, ep)
		}
	}

	for _, ep := range eps {
		if keep(ep) {
			res.Endpoints = append(res.Endpoints, ep)
		}
	}

	for _, ep := range res.Endpoints {
		switch ep.Name {
		case "openai-models", "anthropic-models":
			parseOpenAIModels(&res, ep.Body)
		case "ollama-tags":
			parseOllamaTags(&res, ep.Body, base)
		case "ollama-version":
			parseOllamaVersion(&res, ep.Body)
		case "lmstudio-models":
			parseLMStudio(&res, ep.Body)
		case "llamacpp-props":
			parseProps(&res, ep.Body)
		case "vllm-version":
			parseVLLMVersion(&res, ep.Body)
		case "sglang-model":
			parseSGLang(&res, ep.Body)
		case "comfyui-stats", "comfyui-queue":
			parseComfyUI(&res, ep.Name, ep.Body)
		}
	}

	classify(&res)
	sortEndpoints(res.Endpoints)
	return res
}

func sortEndpoints(eps []Endpoint) {
	sort.Slice(eps, func(i, j int) bool { return eps[i].Name < eps[j].Name })
}

var engineHeaderHints = []string{
	"llama.cpp", "ollama", "vllm", "lmstudio", "litellm", "localai",
	"sglang", "tabbyapi", "text-generation-webui", "huggingface", "tgi",
	"jan", "ggml", "llama-server", "xinference", "mistral",
}

// collectHints records Server / X-Powered-By values. A header says what the
// operator wanted you to think, so it is only ever probable evidence.
func collectHints(res *Result, h http.Header) {
	for k, vs := range h {
		lk := strings.ToLower(k)
		if lk != "server" && lk != "x-powered-by" && !strings.Contains(lk, "engine") {
			continue
		}
		for _, v := range vs {
			lv := strings.ToLower(v)
			for _, hint := range engineHeaderHints {
				if strings.Contains(lv, hint) {
					res.consider(strings.Title(strings.TrimSpace(v)), TierProbable, "header", //nolint:staticcheck
						fmt.Sprintf("%s: %s", lk, strings.TrimSpace(v)))
				}
			}
		}
	}
}

func isAnthropicErrorShape(body []byte) bool {
	var m struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	return m.Type == "error"
}

// parseOpenAIModels reads a model list. /v1/models is shared by every
// OpenAI-compatible server, so it proves the surface, not the product.
func parseOpenAIModels(res *Result, body string) {
	var m struct {
		Data []struct {
			ID          string `json:"id"`
			OwnedBy     string `json:"owned_by"`
			MaxModelLen int    `json:"max_model_len"`
			ContextLen  int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil || len(m.Data) == 0 {
		return
	}
	res.OpenAICompat = true
	res.IsLLM = true
	res.surface("openai")
	for _, d := range m.Data {
		if d.ID != "" && !contains(res.Models, d.ID) {
			res.Models = append(res.Models, d.ID)
		}
		if d.MaxModelLen > res.Context {
			res.Context = d.MaxModelLen
		}
		if d.ContextLen > res.Context {
			res.Context = d.ContextLen
		}
		if d.OwnedBy != "" && res.Vendor == "" {
			res.Vendor = d.OwnedBy
		}
		// owned_by is a self-reported field; only vendor-specific values say
		// something about the engine.
		switch strings.ToLower(d.OwnedBy) {
		case "vllm":
			res.consider("vLLM", TierProbable, "payload", "owned_by=vllm")
		case "llama.cpp":
			res.consider("llama.cpp server", TierProbable, "payload", "owned_by=llama.cpp")
		}
	}
}

// parseOllamaTags reads /api/tags. The shape is copied by non-Ollama servers,
// so Ollama is only named when the per-model detail fields are filled in the
// way Ollama fills them.
func parseOllamaTags(res *Result, body, base string) {
	var m struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
				Family            string `json:"family"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil || len(m.Models) == 0 {
		return
	}
	res.IsLLM = true
	res.surface("ollama")
	distinctive := false
	for _, mo := range m.Models {
		if mo.Name != "" {
			res.Models = append(res.Models, mo.Name)
		}
		if mo.Details.ParameterSize != "" {
			res.Params = mo.Details.ParameterSize
		}
		if mo.Details.QuantizationLevel != "" {
			res.Quant = mo.Details.QuantizationLevel
		}
		if mo.Details.Family != "" {
			res.Arch = mo.Details.Family
		}
		if mo.Details.ParameterSize != "" || mo.Details.QuantizationLevel != "" {
			distinctive = true
		}
		if mo.Size > 0 {
			res.Extra = append(res.Extra, KV{K: "size:" + mo.Name, V: humanBytes(mo.Size)})
		}
	}
	if distinctive {
		res.consider("Ollama", TierConfirmed, "payload", "/api/tags fills parameter_size + quantization_level")
	} else {
		res.Evidence = append(res.Evidence,
			"/api/tags has the Ollama shape but leaves the per-model detail fields empty: API surface only")
	}
	// Deep detail per model via POST /api/show (bounded to 3 models).
	n := len(m.Models)
	if n > 3 {
		n = 3
	}
	for _, mo := range m.Models[:n] {
		if mo.Name == "" {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, base+"/api/show",
			bytes.NewReader([]byte(`{"model":`+strconv.Quote(mo.Name)+`}`)))
		if err != nil {
			continue
		}
		req.Header.Set("content-type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		resp.Body.Close()
		res.Endpoints = append(res.Endpoints, Endpoint{
			Name: "ollama-show", Path: "/api/show:" + mo.Name,
			Status: resp.StatusCode, Body: strings.TrimSpace(string(body)),
		})
		parseOllamaShow(res, body)
	}
}

// parseOllamaShow reads POST /api/show. license/parameters/template/modelfile
// are fields Ollama serves; an imitator that answers /api/tags usually leaves
// them empty or omits them.
func parseOllamaShow(res *Result, body []byte) {
	var m struct {
		License    string `json:"license"`
		Parameters string `json:"parameters"`
		Template   string `json:"template"`
		ModelFile  string `json:"modelfile"`
		Details    struct {
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
			Family            string `json:"family"`
		} `json:"details"`
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return
	}
	if m.License != "" || m.Parameters != "" || m.Template != "" || m.ModelFile != "" {
		res.consider("Ollama", TierConfirmed, "payload", "/api/show serves license/parameters/template")
	}
	if m.Details.ParameterSize != "" && res.Params == "" {
		res.Params = m.Details.ParameterSize
	}
	if m.Details.QuantizationLevel != "" && res.Quant == "" {
		res.Quant = m.Details.QuantizationLevel
	}
	if m.Details.Family != "" && res.Arch == "" {
		res.Arch = m.Details.Family
	}
	for k, v := range m.ModelInfo {
		if f, ok := v.(float64); ok && strings.HasSuffix(k, ".context_length") && int(f) > res.Context {
			res.Context = int(f)
		}
		if strings.Contains(k, "quantization") {
			if s, ok := v.(string); ok && res.Quant == "" {
				res.Quant = s
			}
		}
	}
}

// parseOllamaVersion reads /api/version, which several servers copy; it only
// corroborates the Ollama surface.
func parseOllamaVersion(res *Result, body string) {
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil || m.Version == "" {
		return
	}
	res.surface("ollama")
	if res.Version == "" {
		res.Version = m.Version
	}
}

func parseLMStudio(res *Result, body string) {
	var m struct {
		Data []struct {
			ID               string `json:"id"`
			Publisher        string `json:"publisher"`
			Quantization     string `json:"quantization"`
			State            string `json:"state"`
			MaxContextLength int    `json:"max_context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil || len(m.Data) == 0 {
		return
	}
	res.IsLLM = true
	res.surface("lmstudio")
	distinctive := false
	for _, d := range m.Data {
		if d.ID != "" && !contains(res.Models, d.ID) {
			res.Models = append(res.Models, d.ID)
		}
		if d.Quantization != "" && res.Quant == "" {
			res.Quant = d.Quantization
		}
		if d.MaxContextLength > res.Context {
			res.Context = d.MaxContextLength
		}
		if d.Publisher != "" || d.State != "" || d.MaxContextLength > 0 {
			distinctive = true
		}
		if d.State != "" {
			res.Extra = append(res.Extra, KV{K: "state:" + d.ID, V: d.State})
		}
	}
	if distinctive {
		res.consider("LM Studio", TierConfirmed, "payload", "/api/v0/models with publisher/state/max_context_length")
	}
}

func parseProps(res *Result, body string) {
	// Accept only genuine llama.cpp /props payloads; error or auth bodies
	// unmarshal into an all-zero struct, so require a distinctive key.
	var probeShape map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &probeShape); err != nil {
		return
	}
	if _, ok := probeShape["default_generation_settings"]; !ok {
		if _, ok := probeShape["model_path"]; !ok {
			if _, ok := probeShape["total_slots"]; !ok {
				return
			}
		}
	}
	var m struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		ModelPath string `json:"model_path"`
		Model     string `json:"model"`
		Models    []struct {
			Model     string `json:"model"`
			NCtxSlots int    `json:"n_ctx_slots"`
		} `json:"models"`
		Meta struct {
			NCtx int `json:"n_ctx_train"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return
	}
	res.IsLLM = true
	res.surface("llamacpp")
	res.consider("llama.cpp server", TierConfirmed, "payload", "/props payload shape")
	if m.DefaultGenerationSettings.NCtx > res.Context {
		res.Context = m.DefaultGenerationSettings.NCtx
	}
	for _, mm := range m.Models {
		if mm.NCtxSlots > res.Context {
			res.Context = mm.NCtxSlots
		}
		if mm.Model != "" && !contains(res.Models, mm.Model) {
			res.Models = append(res.Models, mm.Model)
		}
	}
	if len(res.Models) == 0 {
		switch {
		case m.Model != "":
			res.Models = append(res.Models, m.Model)
		case m.ModelPath != "":
			res.Models = append(res.Models, baseName(m.ModelPath))
		}
	}
}

// parseVLLMVersion reads /version, which SGLang answers with the same shape.
func parseVLLMVersion(res *Result, body string) {
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil || m.Version == "" {
		return
	}
	res.IsLLM = true
	res.surface("vllm")
	if res.Version == "" {
		res.Version = m.Version
	}
	res.consider("vLLM", TierProbable, "payload", "/version {version} — shared with SGLang")
}

// parseSGLang reads /get_model_info. vLLM answers it too, and only vLLM adds
// tokenizer/model_name.
func parseSGLang(res *Result, body string) {
	var m struct {
		ModelPath string `json:"model_path"`
		Tokenizer string `json:"tokenizer"`
		ModelName string `json:"model_name"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil || m.ModelPath == "" {
		return
	}
	res.IsLLM = true
	res.surface("vllm/sglang")
	if !contains(res.Models, baseName(m.ModelPath)) {
		res.Models = append(res.Models, baseName(m.ModelPath))
	}
	if m.Tokenizer != "" || m.ModelName != "" {
		res.consider("vLLM", TierConfirmed, "payload", "/get_model_info with tokenizer/model_name")
		return
	}
	res.consider("SGLang", TierProbable, "payload", "/get_model_info with model_path only — vLLM answers this too")
}

// parseComfyUI identifies ComfyUI via /system_stats (comfyui_version key) and
// collects version, OS, Python, and GPU device info.
func parseComfyUI(res *Result, name, body string) {
	var m struct {
		System struct {
			OS             string `json:"os"`
			RAMTotal       int64  `json:"ram_total"`
			ComfyUIVersion string `json:"comfyui_version"`
			PythonVersion  string `json:"python_version"`
		} `json:"system"`
		Devices []struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			VRAMTotal int64  `json:"vram_total"`
			VRAMFree  int64  `json:"vram_free"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return
	}
	switch name {
	case "comfyui-stats":
		if m.System.ComfyUIVersion == "" {
			return
		}
		res.IsLLM = true
		res.surface("comfyui")
		res.consider("ComfyUI", TierConfirmed, "payload", "/system_stats.comfyui_version")
		res.Version = m.System.ComfyUIVersion
		if m.System.OS != "" {
			res.Extra = append(res.Extra, KV{K: "os", V: m.System.OS})
		}
		if m.System.PythonVersion != "" {
			res.Extra = append(res.Extra, KV{K: "python", V: m.System.PythonVersion})
		}
		if m.System.RAMTotal > 0 {
			res.Extra = append(res.Extra, KV{K: "ram", V: humanBytes(m.System.RAMTotal)})
		}
		for _, d := range m.Devices {
			gpu := d.Name
			if d.Type != "" {
				gpu += " (" + d.Type + ")"
			}
			if d.VRAMTotal > 0 {
				gpu += " " + humanBytes(d.VRAMTotal)
			}
			res.Extra = append(res.Extra, KV{K: "gpu", V: gpu})
		}
	case "comfyui-queue":
		// Shape check only: {"queue_running":[],"queue_pending":[]} is the
		// ComfyUI queue payload; other apps answering /queue stay unflagged.
		var q struct {
			QueueRunning any `json:"queue_running"`
			QueuePending any `json:"queue_pending"`
		}
		if err := json.Unmarshal([]byte(body), &q); err == nil &&
			q.QueueRunning != nil && q.QueuePending != nil {
			res.surface("comfyui")
			res.consider("ComfyUI", TierProbable, "payload", "/queue shape only")
		}
	}
}

// classify resolves the candidates into one engine label. Two equally strong
// candidates for different products means we cannot tell them apart from HTTP
// alone, so the engine stays unknown and the conflict is recorded.
func classify(res *Result) {
	if len(res.Candidates) > 0 {
		best := res.Candidates[0]
		conflict := false
		for _, c := range res.Candidates[1:] {
			switch {
			case rank(c) > rank(best):
				best = c
				conflict = false
			case rank(c) == rank(best) && c.Engine != best.Engine:
				conflict = true
			}
		}
		if conflict {
			res.Evidence = append(res.Evidence, "conflicting evidence: "+joinCandidates(res.Candidates))
		} else {
			res.Engine = best.Engine
			res.Confidence = best.Tier
			res.Evidence = append(res.Evidence, best.Engine+" ("+best.Tier+"): "+best.Why)
		}
	}
	if res.IsLLM && res.Context > 0 {
		res.Extra = append(res.Extra, KV{K: "context", V: strconv.Itoa(res.Context)})
	}
}

func joinCandidates(cs []Candidate) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, c.Engine+" "+c.Tier)
	}
	return strings.Join(parts, ", ")
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// EngineSummary is the dashboard-facing one-line engine label: a product name
// only when the evidence supports one, "unknown" otherwise. Probable evidence
// is marked with a trailing ?.
func (r Result) EngineSummary() string {
	s := r.Engine
	if s == "" {
		s = "unknown"
	} else if r.Confidence == TierProbable {
		s += "?"
	}
	if r.Version != "" {
		s += " " + r.Version
	}
	var compat []string
	if r.OpenAICompat {
		compat = append(compat, "openai")
	}
	if r.AnthropicCompat {
		compat = append(compat, "anthropic")
	}
	if len(compat) > 0 {
		s += " [" + strings.Join(compat, "+") + "]"
	}
	return s
}
