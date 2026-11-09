package probe

import (
	"net/http"
	"strings"
	"testing"
)

// Payload shapes captured from real servers, plus imitators that answer the
// shared surfaces without being the product they mimic.
const (
	ollamaTagsReal = `{"models":[{"name":"qwen3:8b","size":4959638072,
		"details":{"parameter_size":"8B","quantization_level":"Q4_K_M","family":"qwen3"}}]}`

	// Same envelope, empty detail fields: proves the API, not the product.
	ollamaTagsImitator = `{"models":[{"name":"auto/glm-5.3-flash","size":0,
		"details":{"family":"someproxy","parameter_size":"","quantization_level":""}}]}`

	ollamaShowReal = `{"license":"Apache License Version 2.0","parameters":"","template":"",
		"modelfile":"","model_info":{"general.architecture":"qwen3"}}`

	ollamaShowImitator = `{"capabilities":["completion","tools"],"details":{"family":"someproxy"},
		"model_info":{"general.architecture":"someproxy"}}`

	llamacppProps = `{"default_generation_settings":{"n_ctx":4096},"model_path":"/models/x.gguf","total_slots":4}`

	// /props-shaped error body: no distinctive keys.
	propsError = `{"error":"unauthorized"}`

	openaiModels  = `{"data":[{"id":"x","owned_by":"someone","max_model_len":32768}]}`
	openaiVLLM    = `{"data":[{"id":"x","owned_by":"vllm"}]}`
	lmstudio      = `{"data":[{"id":"x","publisher":"lmstudio","state":"loaded","max_context_length":8192}]}`
	versionOnly   = `{"version":"0.11.0"}`
	modelInfoBare = `{"model_path":"/models/x"}`
	modelInfoVLLM = `{"model_path":"/models/x","tokenizer":"/models/x","model_name":"x"}`
	comfyStats    = `{"system":{"os":"linux","comfyui_version":"0.44.0","python_version":"3.12"}}`
	comfyQueue    = `{"queue_running":[],"queue_pending":[]}`
)

func run(t *testing.T, calls ...func(*Result)) Result {
	t.Helper()
	res := Result{Addr: "127.0.0.1:1", Port: 1}
	for _, c := range calls {
		c(&res)
	}
	classify(&res)
	return res
}

func TestImitatorTagsDoNotNameOllama(t *testing.T) {
	res := run(t, func(r *Result) {
		r.IsLLM = true
		parseOllamaTags(r, ollamaTagsImitator, "http://127.0.0.1:1")
	})
	if res.Engine != "" {
		t.Errorf("engine = %q, want unknown for an Ollama-shaped imitator", res.Engine)
	}
	if res.EngineSummary() != "unknown" {
		t.Errorf("summary = %q, want \"unknown\"", res.EngineSummary())
	}
	if !contains(res.Surface, "ollama") {
		t.Errorf("surface = %v, want the ollama surface recorded", res.Surface)
	}
	if len(res.Evidence) == 0 {
		t.Error("want the reason for staying unknown recorded as evidence")
	}
}

func TestRealOllamaIsConfirmed(t *testing.T) {
	res := run(t, func(r *Result) {
		r.IsLLM = true
		parseOllamaTags(r, ollamaTagsReal, "http://127.0.0.1:1")
	})
	if res.Engine != "Ollama" || res.Confidence != TierConfirmed {
		t.Errorf("got %q/%q, want Ollama/%s", res.Engine, res.Confidence, TierConfirmed)
	}
	if res.EngineSummary() != "Ollama" {
		t.Errorf("summary = %q, want no ? on confirmed evidence", res.EngineSummary())
	}
}

func TestOllamaShowFieldsConfirm(t *testing.T) {
	res := run(t, func(r *Result) {
		r.IsLLM = true
		parseOllamaTags(r, ollamaTagsImitator, "http://127.0.0.1:1")
		parseOllamaShow(r, []byte(ollamaShowReal))
	})
	if res.Engine != "Ollama" || res.Confidence != TierConfirmed {
		t.Errorf("got %q/%q, want Ollama/%s from /api/show fields", res.Engine, res.Confidence, TierConfirmed)
	}

	// An imitator that answers /api/show without Ollama's fields stays unknown.
	res = run(t, func(r *Result) {
		r.IsLLM = true
		parseOllamaTags(r, ollamaTagsImitator, "http://127.0.0.1:1")
		parseOllamaShow(r, []byte(ollamaShowImitator))
	})
	if res.Engine != "" {
		t.Errorf("engine = %q, want unknown when /api/show has no Ollama fields", res.Engine)
	}
}

func TestPropsShapeConfirmsLlamaCPP(t *testing.T) {
	res := run(t, func(r *Result) { parseProps(r, llamacppProps) })
	if res.Engine != "llama.cpp server" || res.Confidence != TierConfirmed {
		t.Errorf("got %q/%q, want llama.cpp server/%s", res.Engine, res.Confidence, TierConfirmed)
	}
	if !res.IsLLM {
		t.Error("a genuine /props payload must flag the port as an LLM endpoint")
	}

	// An error body under /props proves nothing.
	res = run(t, func(r *Result) { parseProps(r, propsError) })
	if res.IsLLM || res.Engine != "" {
		t.Errorf("error body classified as %q/%v", res.Engine, res.IsLLM)
	}
}

func TestSharedVersionAndModelInfoStayUnknown(t *testing.T) {
	// /version is answered by vLLM and SGLang alike; a bare model_path does not
	// break the tie, so two equally strong candidates must cancel out.
	res := run(t, func(r *Result) {
		r.IsLLM = true
		parseVLLMVersion(r, versionOnly)
		parseSGLang(r, modelInfoBare)
	})
	if res.Engine != "" {
		t.Errorf("engine = %q, want unknown for ambiguous vLLM/SGLang evidence", res.Engine)
	}
	if !strings.Contains(strings.Join(res.Evidence, "\n"), "conflicting") {
		t.Errorf("evidence = %v, want the conflict recorded", res.Evidence)
	}

	// vLLM-only keys in /get_model_info settle it.
	res = run(t, func(r *Result) {
		r.IsLLM = true
		parseVLLMVersion(r, versionOnly)
		parseSGLang(r, modelInfoVLLM)
	})
	if res.Engine != "vLLM" || res.Confidence != TierConfirmed {
		t.Errorf("got %q/%q, want vLLM/%s", res.Engine, res.Confidence, TierConfirmed)
	}
}

func TestOpenAIModelsAloneNeverNameAProduct(t *testing.T) {
	res := run(t, func(r *Result) { parseOpenAIModels(r, openaiModels) })
	if res.Engine != "" {
		t.Errorf("engine = %q, want unknown for a plain /v1/models", res.Engine)
	}
	if !res.OpenAICompat || res.Vendor != "someone" {
		t.Errorf("compat/vendor = %v/%q", res.OpenAICompat, res.Vendor)
	}

	// A vendor-specific owned_by is still only probable evidence.
	res = run(t, func(r *Result) { parseOpenAIModels(r, openaiVLLM) })
	if res.Engine != "vLLM" || res.Confidence != TierProbable {
		t.Errorf("got %q/%q, want vLLM/%s", res.Engine, res.Confidence, TierProbable)
	}
	if res.EngineSummary() != "vLLM? [openai]" {
		t.Errorf("summary = %q, want the probable marker", res.EngineSummary())
	}
}

func TestLMStudioShapeConfirms(t *testing.T) {
	res := run(t, func(r *Result) { parseLMStudio(r, lmstudio) })
	if res.Engine != "LM Studio" || res.Confidence != TierConfirmed {
		t.Errorf("got %q/%q, want LM Studio/%s", res.Engine, res.Confidence, TierConfirmed)
	}
}

func TestComfyUIStatsConfirmsQueueAloneIsProbable(t *testing.T) {
	res := run(t, func(r *Result) { parseComfyUI(r, "comfyui-stats", comfyStats) })
	if res.Engine != "ComfyUI" || res.Confidence != TierConfirmed {
		t.Errorf("got %q/%q, want ComfyUI/%s", res.Engine, res.Confidence, TierConfirmed)
	}
	res = run(t, func(r *Result) { parseComfyUI(r, "comfyui-queue", comfyQueue) })
	if res.Engine != "ComfyUI" || res.Confidence != TierProbable {
		t.Errorf("got %q/%q, want ComfyUI/%s from the queue shape alone", res.Engine, res.Confidence, TierProbable)
	}
}

func TestHeaderHintsAreProbableOnly(t *testing.T) {
	res := run(t, func(r *Result) {
		r.IsLLM = true
		collectHints(r, http.Header{"Server": []string{"llama.cpp"}})
	})
	if res.Confidence != TierProbable {
		t.Errorf("confidence = %q, want %s for a header hint", res.Confidence, TierProbable)
	}

	// A payload shape outranks a header naming something else.
	res = run(t, func(r *Result) {
		r.IsLLM = true
		collectHints(r, http.Header{"Server": []string{"nginx"}})
		parseProps(r, llamacppProps)
	})
	if res.Engine != "llama.cpp server" {
		t.Errorf("engine = %q, want the payload shape to win", res.Engine)
	}
}

func TestConfirmedBeatsProbableAcrossCandidates(t *testing.T) {
	res := run(t, func(r *Result) {
		r.IsLLM = true
		parseVLLMVersion(r, versionOnly) // probable vLLM
		parseProps(r, llamacppProps)     // confirmed llama.cpp
	})
	if res.Engine != "llama.cpp server" {
		t.Errorf("engine = %q, want the confirmed candidate", res.Engine)
	}
}
