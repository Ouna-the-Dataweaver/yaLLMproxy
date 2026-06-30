package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/modules"
)

const (
	maxTemplateUploadBytes  = 2 << 20
	maxTemplatePreviewBytes = 32 << 10
	templatesPublicPrefix   = "configs/jinja_templates"
)

var (
	adminTemplatesDir     = filepath.Join(config.ProjectRoot(), "configs", "jinja_templates")
	templatePathSegmentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	errTemplateNotFound   = errors.New("template not found")
)

type adminTemplateEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size,omitempty"`
}

func (a *App) handleListTemplates(w http.ResponseWriter, _ *http.Request) {
	templates, err := listAdminTemplates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "template_list_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

func (a *App) handleUploadTemplate(w http.ResponseWriter, r *http.Request) {
	if adminPasswordConfigured() && !a.hasAdminPassword(r) {
		writeError(w, http.StatusForbidden, "admin password required for template upload", "permission_error")
		return
	}
	name, content, err := readTemplateUpload(w, r)
	if err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, err.Error(), "invalid_template_upload")
		return
	}
	if len(content) == 0 {
		writeError(w, http.StatusBadRequest, "template content is required", "missing_parameter")
		return
	}
	if !utf8.Valid(content) {
		writeError(w, http.StatusBadRequest, "template content must be valid UTF-8", "invalid_template_content")
		return
	}
	target, rel, err := resolveWritableAdminTemplatePath(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_template_path")
		return
	}
	if err := ensureTemplateWritableParent(filepath.Dir(target)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_template_path")
		return
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		writeError(w, http.StatusBadRequest, "template path must not be a symlink", "invalid_template_path")
		return
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "template_save_failed")
		return
	}
	writeJSON(w, http.StatusOK, adminTemplateEntry{
		Name: rel,
		Path: templatePublicPath(rel),
		Size: int64(len(content)),
	})
}

func (a *App) handleInspectTemplate(w http.ResponseWriter, r *http.Request) {
	templatePath := firstNonEmpty(
		r.URL.Query().Get("template_path"),
		r.URL.Query().Get("path"),
		r.URL.Query().Get("name"),
	)
	resolved, rel, err := resolveExistingAdminTemplatePath(templatePath)
	if err != nil {
		if errors.Is(err, errTemplateNotFound) {
			writeError(w, http.StatusNotFound, "template not found", "not_found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_template_path")
		return
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read template: %v", err), "template_read_failed")
		return
	}
	if !utf8.Valid(raw) {
		writeError(w, http.StatusBadRequest, "template content must be valid UTF-8", "invalid_template_content")
		return
	}
	analysis := modules.AnalyzeTemplateText(string(raw))
	cfg, detected := analysis.Config, analysis.Detected
	cfgPayload := parseTagsConfigPayload(cfg)
	payload := map[string]any{
		"name":              rel,
		"path":              templatePublicPath(rel),
		"template_path":     templatePublicPath(rel),
		"detected":          detected,
		"config":            cfgPayload,
		"parse_tags_config": cfgPayload,
		"evidence":          analysis.Evidence,
		"warnings":          analysis.Warnings,
		"analysis": map[string]any{
			"engine":     "bounded_jinja_inspection",
			"full_jinja": false,
		},
	}
	if cfg.ThinkTag != "" {
		payload["detected_think_tag"] = cfg.ThinkTag
	}
	if templateRenderPreviewRequested(r) {
		payload["render_preview"] = templateRenderPreview(string(raw))
	}
	writeJSON(w, http.StatusOK, payload)
}

func templateRenderPreviewRequested(r *http.Request) bool {
	for _, key := range []string{"render_preview", "preview_render", "preview"} {
		switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func templateRenderPreview(text string) map[string]any {
	result, err := modules.RenderJinjaTemplate(text, modules.DefaultRuntimeJinjaChatContext(), modules.RuntimeJinjaRenderOptions{
		MaxOutputBytes:    maxTemplatePreviewBytes,
		MaxRenderDepth:    32,
		MaxLoopIterations: 1024,
	})
	preview := map[string]any{
		"engine":     modules.RuntimeJinjaEngine,
		"full_jinja": false,
		"truncated":  result.Truncated,
		"warnings":   result.Warnings,
	}
	if result.Rendered != "" {
		preview["rendered"] = result.Rendered
	}
	if err != nil {
		preview["error"] = err.Error()
	}
	return preview
}

func listAdminTemplates() ([]adminTemplateEntry, error) {
	base, err := adminTemplatesAbsDir()
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []adminTemplateEntry{}, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return []adminTemplateEntry{}, nil
	}

	templates := []adminTemplateEntry{}
	err = filepath.WalkDir(base, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath == base {
				return nil
			}
			rel, err := filepath.Rel(base, filePath)
			if err != nil {
				return filepath.SkipDir
			}
			if _, err := normalizeTemplateRelativePath(filepath.ToSlash(rel)); err != nil {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(base, filePath)
		if err != nil {
			return err
		}
		cleanRel, err := cleanTemplateRelativePath(filepath.ToSlash(rel), false)
		if err != nil {
			return nil
		}
		templates = append(templates, adminTemplateEntry{
			Name: cleanRel,
			Path: templatePublicPath(cleanRel),
			Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(templates, func(i, j int) bool {
		return templates[i].Name < templates[j].Name
	})
	return templates, nil
}

func readTemplateUpload(w http.ResponseWriter, r *http.Request) (string, []byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateUploadBytes)
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if mediaType == "multipart/form-data" {
		return readMultipartTemplateUpload(r)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, err
	}
	if mediaType == "application/json" {
		var payload struct {
			Name     string `json:"name"`
			Filename string `json:"filename"`
			Path     string `json:"path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", nil, fmt.Errorf("invalid JSON payload: %w", err)
		}
		name := firstNonEmpty(payload.Name, payload.Filename, payload.Path)
		if strings.TrimSpace(name) == "" {
			return "", nil, fmt.Errorf("template name is required")
		}
		return name, []byte(payload.Content), nil
	}
	name := firstNonEmpty(r.URL.Query().Get("name"), r.URL.Query().Get("filename"), r.URL.Query().Get("path"))
	if strings.TrimSpace(name) == "" {
		return "", nil, fmt.Errorf("template name is required")
	}
	return name, body, nil
}

func readMultipartTemplateUpload(r *http.Request) (string, []byte, error) {
	if err := r.ParseMultipartForm(maxTemplateUploadBytes); err != nil {
		return "", nil, err
	}
	if content := r.FormValue("content"); content != "" {
		name := firstNonEmpty(r.FormValue("name"), r.FormValue("filename"), r.FormValue("path"))
		if strings.TrimSpace(name) == "" {
			return "", nil, fmt.Errorf("template name is required")
		}
		return name, []byte(content), nil
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return "", nil, fmt.Errorf("file field is required")
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		return "", nil, err
	}
	name := firstNonEmpty(r.FormValue("name"), r.FormValue("filename"), r.FormValue("path"))
	if strings.TrimSpace(name) == "" && header != nil {
		name = header.Filename
	}
	if strings.TrimSpace(name) == "" {
		return "", nil, fmt.Errorf("template name is required")
	}
	return name, content, nil
}

func resolveWritableAdminTemplatePath(raw string) (string, string, error) {
	return resolveAdminTemplatePath(raw, true)
}

func resolveExistingAdminTemplatePath(raw string) (string, string, error) {
	target, rel, err := resolveAdminTemplatePath(raw, false)
	if err != nil {
		return "", "", err
	}
	info, err := os.Lstat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", errTemplateNotFound
		}
		return "", "", err
	}
	if info.IsDir() {
		return "", "", errTemplateNotFound
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", err
	}
	base, err := adminTemplatesEvalDir()
	if err != nil {
		return "", "", err
	}
	if err := ensurePathWithinBase(base, resolved); err != nil {
		return "", "", err
	}
	info, err = os.Stat(resolved)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", errTemplateNotFound
	}
	return resolved, rel, nil
}

func resolveAdminTemplatePath(raw string, addDefaultExt bool) (string, string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", "", fmt.Errorf("template path is required")
	}
	base, err := adminTemplatesAbsDir()
	if err != nil {
		return "", "", err
	}
	if filepath.IsAbs(raw) {
		target := filepath.Clean(raw)
		if err := ensurePathWithinBase(base, target); err != nil {
			return "", "", err
		}
		rel, err := filepath.Rel(base, target)
		if err != nil {
			return "", "", err
		}
		cleanRel, err := cleanTemplateRelativePath(filepath.ToSlash(rel), addDefaultExt)
		if err != nil {
			return "", "", err
		}
		return filepath.Join(base, filepath.FromSlash(cleanRel)), cleanRel, nil
	}
	cleanRel, err := cleanTemplateRelativePath(raw, addDefaultExt)
	if err != nil {
		return "", "", err
	}
	target := filepath.Join(base, filepath.FromSlash(cleanRel))
	if err := ensurePathWithinBase(base, target); err != nil {
		return "", "", err
	}
	return target, cleanRel, nil
}

func cleanTemplateRelativePath(raw string, addDefaultExt bool) (string, error) {
	rel, err := normalizeTemplateRelativePath(raw)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(path.Ext(rel))
	if ext == "" && addDefaultExt {
		rel += ".jinja"
		ext = ".jinja"
	}
	if !validTemplateExtension(ext) {
		return "", fmt.Errorf("template filename must end with .jinja, .j2, or .jinja2")
	}
	return rel, nil
}

func normalizeTemplateRelativePath(raw string) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	if value == "" {
		return "", fmt.Errorf("template path is required")
	}
	if path.IsAbs(value) || filepath.IsAbs(value) {
		return "", fmt.Errorf("template path must be relative to configs/jinja_templates")
	}
	prefix := templatesPublicPrefix + "/"
	if value == templatesPublicPrefix {
		return "", fmt.Errorf("template path is required")
	}
	if strings.HasPrefix(value, prefix) {
		value = strings.TrimPrefix(value, prefix)
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("template path must stay inside configs/jinja_templates")
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid template path segment")
		}
		if !templatePathSegmentRE.MatchString(part) {
			return "", fmt.Errorf("template path segment %q contains unsupported characters", part)
		}
	}
	return clean, nil
}

func validTemplateExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jinja", ".j2", ".jinja2":
		return true
	default:
		return false
	}
}

func templatePublicPath(rel string) string {
	return templatesPublicPrefix + "/" + filepath.ToSlash(rel)
}

func adminTemplatesAbsDir() (string, error) {
	return filepath.Abs(adminTemplatesDir)
}

func adminTemplatesEvalDir() (string, error) {
	base, err := adminTemplatesAbsDir()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return base, nil
	}
	return resolved, nil
}

func ensureTemplateWritableParent(parent string) error {
	base, err := adminTemplatesEvalDir()
	if err != nil {
		return err
	}
	baseAbs, err := adminTemplatesAbsDir()
	if err != nil {
		return err
	}
	parent = filepath.Clean(parent)
	if err := ensurePathWithinBase(baseAbs, parent); err != nil {
		return err
	}
	if info, err := os.Lstat(baseAbs); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("template base path must not be a symlink")
		}
		if !info.IsDir() {
			return fmt.Errorf("template base path is not a directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(baseAbs, 0o755); err != nil {
			return err
		}
	} else {
		return err
	}
	rel, err := filepath.Rel(baseAbs, parent)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("template path must stay inside configs/jinja_templates")
	}
	current := baseAbs
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			switch {
			case err == nil:
				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("template parent path must not contain symlinks")
				}
				if !info.IsDir() {
					return fmt.Errorf("template parent path is not a directory")
				}
			case errors.Is(err, os.ErrNotExist):
				if err := os.Mkdir(current, 0o755); err != nil {
					return err
				}
			default:
				return err
			}
		}
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	return ensurePathWithinBase(base, resolvedParent)
}

func ensurePathWithinBase(base string, target string) error {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("template path must stay inside configs/jinja_templates")
	}
	return nil
}

func parseTagsConfigPayload(cfg modules.ParseTagsConfig) map[string]any {
	out := map[string]any{}
	if cfg.ThinkTag != "" {
		out["think_tag"] = cfg.ThinkTag
		out["think_open"] = map[string]any{"prefix": "<" + cfg.ThinkTag + ">", "suffix": ""}
		out["think_close"] = map[string]any{"prefix": "</" + cfg.ThinkTag + ">", "suffix": ""}
	}
	if cfg.ToolTag != "" {
		out["tool_tag"] = cfg.ToolTag
	}
	if cfg.ToolOpen != "" {
		out["tool_open"] = cfg.ToolOpen
	}
	if cfg.ToolClose != "" {
		out["tool_close"] = cfg.ToolClose
	}
	if cfg.ToolArgFormat != "" {
		out["tool_arg_format"] = cfg.ToolArgFormat
	}
	if cfg.ToolArgSeparator != "" {
		out["tool_arg_separator"] = cfg.ToolArgSeparator
	}
	if cfg.ToolParser != "" {
		out["tool_parser"] = cfg.ToolParser
	}
	if cfg.ToolBufferLimit > 0 {
		out["tool_buffer_limit"] = cfg.ToolBufferLimit
	}
	if len(cfg.DropTags) > 0 {
		out["drop_tags"] = cfg.DropTags
	}
	if cfg.ParseThinking {
		out["parse_thinking"] = cfg.ParseThinking
	}
	if cfg.ParseToolCalls {
		out["parse_tool_calls"] = cfg.ParseToolCalls
	}
	if cfg.StreamToolCalls {
		out["stream_tool_calls"] = cfg.StreamToolCalls
	}
	if cfg.StartInThink {
		out["start_in_think"] = cfg.StartInThink
	}
	return out
}

func adminPasswordConfigured() bool {
	return strings.TrimSpace(os.Getenv(config.AdminPasswordEnv)) != ""
}
