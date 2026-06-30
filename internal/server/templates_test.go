package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func useTempTemplatesDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := adminTemplatesDir
	adminTemplatesDir = dir
	t.Cleanup(func() { adminTemplatesDir = old })
	return dir
}

func TestAdminTemplatesList(t *testing.T) {
	useTempTemplatesDir(t, map[string]string{
		"qwen.jinja":        "<think></think>",
		"nested/k2.jinja2":  "<|tool_call_begin|>",
		"readme.txt":        "ignored",
		".hidden.jinja":     "ignored",
		"bad name.jinja":    "ignored",
		"nested/notes.json": "ignored",
	})
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	templates := payload["templates"].([]any)
	if len(templates) != 2 {
		t.Fatalf("templates = %#v", templates)
	}
	first := templates[0].(map[string]any)
	second := templates[1].(map[string]any)
	if first["name"] != "nested/k2.jinja2" || first["path"] != "configs/jinja_templates/nested/k2.jinja2" {
		t.Fatalf("first template = %#v", first)
	}
	if second["name"] != "qwen.jinja" || second["path"] != "configs/jinja_templates/qwen.jinja" {
		t.Fatalf("second template = %#v", second)
	}
}

func TestAdminTemplatesListEmptyWhenDirectoryMissing(t *testing.T) {
	old := adminTemplatesDir
	adminTemplatesDir = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { adminTemplatesDir = old })

	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload["templates"].([]any); len(got) != 0 {
		t.Fatalf("templates = %#v", got)
	}
}

func TestAdminTemplatesUploadJSONCreatesTemplate(t *testing.T) {
	dir := useTempTemplatesDir(t, nil)
	app := newTestApp(t, "http://upstream.test/v1")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(`{
		"name": "custom",
		"content": "{{ '<analysis>' + reasoning_content + '</analysis>' }} <function_call>x</function_call>"
	}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["name"] != "custom.jinja" || payload["path"] != "configs/jinja_templates/custom.jinja" {
		t.Fatalf("payload = %#v", payload)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "custom.jinja"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "reasoning_content") {
		t.Fatalf("saved template = %s", saved)
	}

	resp = httptest.NewRecorder()
	rawBody := "{{ bos_token }}{% for message in messages %}{{ message.content }}{% endfor %}"
	req = httptest.NewRequest(http.MethodPost, "/admin/templates?name=raw.jinja", strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "text/plain")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("raw status = %d body=%s", resp.Code, resp.Body.String())
	}
	saved, err = os.ReadFile(filepath.Join(dir, "raw.jinja"))
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != rawBody {
		t.Fatalf("raw saved template = %s", saved)
	}
}

func TestAdminTemplatesUploadRequiresPasswordWhenConfigured(t *testing.T) {
	dir := useTempTemplatesDir(t, nil)
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newTestApp(t, "http://upstream.test/v1")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(`{"name":"locked.jinja","content":"<think></think>"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("missing password status = %d body=%s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(`{"name":"locked.jinja","content":"<think></think>"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("password status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "locked.jinja")); err != nil {
		t.Fatal(err)
	}
}

func TestAdminTemplatesUploadRejectsTraversalAndInvalidUTF8(t *testing.T) {
	dir := useTempTemplatesDir(t, nil)
	app := newTestApp(t, "http://upstream.test/v1")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(`{"name":"../evil.jinja","content":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("traversal status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "evil.jinja")); !os.IsNotExist(err) {
		t.Fatalf("unexpected outside file err=%v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "bad.jinja")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte{0xff, 0xfe}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	resp = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin/templates", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 status = %d body=%s", resp.Code, resp.Body.String())
	}

	outside := t.TempDir()
	link := filepath.Join(dir, "linked")
	if err := os.Symlink(outside, link); err == nil {
		resp = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(`{"name":"linked/newdir/escape.jinja","content":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("symlinked parent upload status = %d body=%s", resp.Code, resp.Body.String())
		}
		if _, err := os.Stat(filepath.Join(outside, "newdir")); !os.IsNotExist(err) {
			t.Fatalf("upload created directory outside template dir err=%v", err)
		}
	}
}

func TestAdminTemplatesInspectDerivesParseTagsConfig(t *testing.T) {
	useTempTemplatesDir(t, map[string]string{
		"analysis.jinja": `{{ '<analysis>' + reasoning_content + '</analysis>' }} <function_call>x</function_call>`,
		"k2.jinja":       `<|tool_calls_section_begin|><|tool_call_begin|>name<|tool_call_argument_begin|>{}<|tool_call_end|><|tool_calls_section_end|>`,
		"qwen.jinja":     `{{ '<tool_call>\n<function=' + tool_call.name + '>\n<parameter=' + args_name + '>\nvalue\n</parameter>\n</function>\n</tool_call>' }}`,
	})
	app := newTestApp(t, "http://upstream.test/v1")

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?template_path=configs/jinja_templates/analysis.jinja", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["detected"] != true || payload["detected_think_tag"] != "analysis" {
		t.Fatalf("payload = %#v", payload)
	}
	cfg := payload["config"].(map[string]any)
	if cfg["think_tag"] != "analysis" || cfg["tool_tag"] != "function_call" || cfg["tool_arg_format"] != "xml" {
		t.Fatalf("config = %#v", cfg)
	}
	open := cfg["think_open"].(map[string]any)
	if open["prefix"] != "<analysis>" {
		t.Fatalf("think_open = %#v", open)
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?name=k2.jinja", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("k2 status = %d body=%s", resp.Code, resp.Body.String())
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	cfg = payload["config"].(map[string]any)
	if cfg["tool_open"] != "<|tool_call_begin|>" || cfg["tool_arg_separator"] != "<|tool_call_argument_begin|>" || cfg["tool_arg_format"] != "json" {
		t.Fatalf("k2 config = %#v", cfg)
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?name=qwen.jinja", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("qwen status = %d body=%s", resp.Code, resp.Body.String())
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	cfg = payload["config"].(map[string]any)
	if cfg["tool_parser"] != "qwen_xml" || cfg["tool_open"] != "<tool_call>" || cfg["tool_close"] != "</tool_call>" || cfg["tool_arg_format"] != "xml" {
		t.Fatalf("qwen config = %#v", cfg)
	}
}

func TestAdminTemplatesInspectExposesRenderedEvidenceAndWarnings(t *testing.T) {
	useTempTemplatesDir(t, map[string]string{
		"rendered.jinja": `
{% set open = '<' + 'think' + '>' %}
{% set close = '</' + 'think' + '>' %}
{% for message in messages %}
{% if message.role == 'assistant' %}
{{ bos_token + open + message.reasoning_content + close }}
{% endif %}
{% endfor %}
{% include 'unsupported.jinja' %}
`,
	})
	app := newTestApp(t, "http://upstream.test/v1")

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?name=rendered.jinja", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["detected"] != true || payload["detected_think_tag"] != "think" {
		t.Fatalf("payload = %#v", payload)
	}
	if !hasInspectEvidence(payload, "think_tag", "think", "rendered_inspection") {
		t.Fatalf("missing rendered think evidence: %#v", payload["evidence"])
	}
	if !hasInspectEvidence(payload, "special_token", "<s>", "rendered_inspection") {
		t.Fatalf("missing rendered special-token evidence: %#v", payload["evidence"])
	}
	if !hasInspectWarning(payload, "unsupported Jinja statement ignored") {
		t.Fatalf("missing bounded-render warning: %#v", payload["warnings"])
	}
	analysis := payload["analysis"].(map[string]any)
	if analysis["engine"] != "bounded_jinja_inspection" || analysis["full_jinja"] != false {
		t.Fatalf("analysis metadata = %#v", analysis)
	}
	if _, exists := payload["render_preview"]; exists {
		t.Fatalf("render_preview should be absent without opt-in: %#v", payload["render_preview"])
	}
}

func TestAdminTemplatesInspectOptionalRuntimePreview(t *testing.T) {
	useTempTemplatesDir(t, map[string]string{
		"preview.jinja":     `{% for message in messages %}{{ message.role }}:{{ message.content | default('', true) | trim }};{% endfor %}{% if add_generation_prompt %}assistant:{% endif %}`,
		"bad-preview.jinja": `{% include 'missing.jinja' %}<think></think>`,
	})
	app := newTestApp(t, "http://upstream.test/v1")

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?name=preview.jinja&render_preview=1", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	analysis := payload["analysis"].(map[string]any)
	if analysis["engine"] != "bounded_jinja_inspection" || analysis["full_jinja"] != false {
		t.Fatalf("analysis metadata = %#v", analysis)
	}
	preview := payload["render_preview"].(map[string]any)
	if preview["engine"] != "bounded_jinja_runtime" || preview["full_jinja"] != false || preview["truncated"] != false {
		t.Fatalf("preview metadata = %#v", preview)
	}
	rendered := fmt.Sprint(preview["rendered"])
	if !strings.Contains(rendered, "system:You are a concise assistant.;") || !strings.Contains(rendered, "user:Find cats.;") || !strings.Contains(rendered, "assistant:") {
		t.Fatalf("preview rendered = %q", rendered)
	}
	if _, exists := preview["error"]; exists {
		t.Fatalf("unexpected preview error: %#v", preview)
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?name=bad-preview.jinja&render_preview=true", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("bad preview status = %d body=%s", resp.Code, resp.Body.String())
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	preview = payload["render_preview"].(map[string]any)
	if !strings.Contains(fmt.Sprint(preview["error"]), "unsupported Jinja statement") {
		t.Fatalf("preview error = %#v", preview)
	}
	analysis = payload["analysis"].(map[string]any)
	if analysis["engine"] != "bounded_jinja_inspection" {
		t.Fatalf("analysis metadata changed after preview error: %#v", analysis)
	}
}

func TestAdminTemplatesInspectRejectsTraversalAndMissingFile(t *testing.T) {
	dir := useTempTemplatesDir(t, map[string]string{"qwen.jinja": "<think></think>"})
	app := newTestApp(t, "http://upstream.test/v1")

	for _, query := range []string{
		"template_path=../../etc/passwd",
		"template_path=/etc/passwd",
		"template_path=../qwen.jinja",
	} {
		resp := httptest.NewRecorder()
		app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?"+query, nil))
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s", query, resp.Code, resp.Body.String())
		}
	}

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?template_path=missing.jinja", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d body=%s", resp.Code, resp.Body.String())
	}

	outside := filepath.Join(t.TempDir(), "outside.jinja")
	if err := os.WriteFile(outside, []byte("<think></think>"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.jinja")
	if err := os.Symlink(outside, link); err == nil {
		resp = httptest.NewRecorder()
		app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?template_path=linked.jinja", nil))
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("symlink status = %d body=%s", resp.Code, resp.Body.String())
		}
	}

	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.jinja"), []byte("<think></think>"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(dir, "linked-dir")
	if err := os.Symlink(outsideDir, dirLink); err == nil {
		resp = httptest.NewRecorder()
		app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/templates/inspect?template_path=linked-dir/secret.jinja", nil))
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("symlinked parent inspect status = %d body=%s", resp.Code, resp.Body.String())
		}
	}
}

func hasInspectEvidence(payload map[string]any, kind string, value string, source string) bool {
	items, _ := payload["evidence"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["kind"] == kind && item["value"] == value && item["source"] == source {
			return true
		}
	}
	return false
}

func hasInspectWarning(payload map[string]any, needle string) bool {
	items, _ := payload["warnings"].([]any)
	for _, raw := range items {
		if strings.Contains(fmt.Sprint(raw), needle) {
			return true
		}
	}
	return false
}
