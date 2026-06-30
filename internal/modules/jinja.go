package modules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type TemplateAnalysis struct {
	Config   ParseTagsConfig    `json:"-"`
	Detected bool               `json:"detected"`
	Evidence []TemplateEvidence `json:"evidence,omitempty"`
	Warnings []string           `json:"warnings,omitempty"`
}

type TemplateEvidence struct {
	Kind    string `json:"kind"`
	Value   string `json:"value,omitempty"`
	Source  string `json:"source"`
	Snippet string `json:"snippet,omitempty"`
}

const (
	jinjaInspectionMaxOutputBytes    = 256 << 10
	jinjaInspectionMaxRenderDepth    = 64
	jinjaInspectionMaxLoopIterations = 512
)

func AnalyzeTemplateText(text string) TemplateAnalysis {
	rendered, warnings := renderJinjaInspectionText(text)
	combined := text
	if strings.TrimSpace(rendered) != "" {
		combined += "\n" + rendered
	}
	cfg, detected := deriveParseTagsConfigFromTemplateText(combined)
	return TemplateAnalysis{
		Config:   cfg,
		Detected: detected,
		Evidence: collectTemplateEvidence(text, rendered),
		Warnings: warnings.sorted(),
	}
}

type warningSet map[string]struct{}

func (w warningSet) add(msg string) {
	msg = strings.TrimSpace(msg)
	if msg != "" {
		w[msg] = struct{}{}
	}
}

func (w warningSet) sorted() []string {
	if len(w) == 0 {
		return nil
	}
	out := make([]string, 0, len(w))
	for msg := range w {
		out = append(out, msg)
	}
	sort.Strings(out)
	return out
}

type jinjaToken struct {
	kind string
	body string
	raw  string
}

type jinjaRenderBudget struct {
	outputBytes    int
	loopIterations int
}

func (b *jinjaRenderBudget) writeString(out *strings.Builder, value string, warnings warningSet) {
	if value == "" {
		return
	}
	remaining := jinjaInspectionMaxOutputBytes - b.outputBytes
	if remaining <= 0 {
		warnings.add("bounded Jinja inspection output limit reached; rendered output truncated")
		return
	}
	if len(value) > remaining {
		out.WriteString(value[:remaining])
		b.outputBytes += remaining
		warnings.add("bounded Jinja inspection output limit reached; rendered output truncated")
		return
	}
	out.WriteString(value)
	b.outputBytes += len(value)
}

func (b *jinjaRenderBudget) consumeLoopIteration(warnings warningSet) bool {
	if b.loopIterations >= jinjaInspectionMaxLoopIterations {
		warnings.add("bounded Jinja inspection loop iteration limit reached; remaining loop body renders skipped")
		return false
	}
	b.loopIterations++
	return true
}

func renderJinjaInspectionText(text string) (string, warningSet) {
	warnings := warningSet{}
	tokens := tokenizeJinja(text, warnings)
	var rendered []string
	for _, env := range inspectionEnvironments() {
		out := renderJinjaTokens(tokens, cloneJinjaEnv(env), warnings, &jinjaRenderBudget{}, 0)
		if strings.TrimSpace(out) != "" {
			rendered = append(rendered, out)
		}
	}
	return strings.Join(rendered, "\n"), warnings
}

func tokenizeJinja(text string, warnings warningSet) []jinjaToken {
	tokens := []jinjaToken{}
	for len(text) > 0 {
		idx := -1
		open := ""
		for _, candidate := range []string{"{{", "{%", "{#"} {
			if at := strings.Index(text, candidate); at >= 0 && (idx < 0 || at < idx) {
				idx = at
				open = candidate
			}
		}
		if idx < 0 {
			tokens = append(tokens, jinjaToken{kind: "text", raw: text})
			break
		}
		if idx > 0 {
			tokens = append(tokens, jinjaToken{kind: "text", raw: text[:idx]})
			text = text[idx:]
		}
		close := map[string]string{"{{": "}}", "{%": "%}", "{#": "#}"}[open]
		end := strings.Index(text[len(open):], close)
		if end < 0 {
			warnings.add("malformed Jinja marker: unterminated " + open)
			tokens = append(tokens, jinjaToken{kind: "text", raw: text})
			break
		}
		raw := text[:len(open)+end+len(close)]
		body := strings.TrimSpace(text[len(open) : len(open)+end])
		body = strings.TrimPrefix(body, "-")
		body = strings.TrimSuffix(body, "-")
		body = strings.TrimSpace(body)
		kind := "expr"
		if open == "{%" {
			kind = "stmt"
		} else if open == "{#" {
			kind = "comment"
		}
		tokens = append(tokens, jinjaToken{kind: kind, body: body, raw: raw})
		text = text[len(raw):]
	}
	return tokens
}

func renderJinjaTokens(tokens []jinjaToken, env map[string]any, warnings warningSet, budget *jinjaRenderBudget, depth int) string {
	if depth > jinjaInspectionMaxRenderDepth {
		warnings.add("bounded Jinja inspection render depth limit reached; nested block skipped")
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		switch token.kind {
		case "text":
			budget.writeString(&out, token.raw, warnings)
		case "comment":
			continue
		case "expr":
			value, ok := evalJinjaValue(token.body, env, warnings)
			if !ok {
				warnings.add("unsupported Jinja expression: " + compactJinjaSnippet(token.body))
				continue
			}
			budget.writeString(&out, jinjaString(value), warnings)
		case "stmt":
			stmt := strings.TrimSpace(token.body)
			switch {
			case strings.HasPrefix(stmt, "for "):
				bodyStart := i + 1
				bodyEnd := findMatchingJinjaBlock(tokens, i, "for", "endfor")
				if bodyEnd < 0 {
					warnings.add("malformed Jinja for block: missing endfor")
					continue
				}
				renderJinjaFor(stmt, tokens[bodyStart:bodyEnd], env, warnings, budget, depth+1, &out)
				i = bodyEnd
			case strings.HasPrefix(stmt, "if "):
				bodyEnd := findMatchingJinjaBlock(tokens, i, "if", "endif")
				if bodyEnd < 0 {
					warnings.add("malformed Jinja if block: missing endif")
					continue
				}
				out.WriteString(renderJinjaIf(stmt, tokens[i+1:bodyEnd], env, warnings, budget, depth+1))
				i = bodyEnd
			case strings.HasPrefix(stmt, "set "):
				applyJinjaSet(stmt, env, warnings)
			case strings.HasPrefix(stmt, "macro "):
				bodyEnd := findMatchingJinjaBlock(tokens, i, "macro", "endmacro")
				warnings.add("unsupported Jinja macro block skipped during inspection render")
				if bodyEnd >= 0 {
					i = bodyEnd
				}
			case stmt == "break" || stmt == "continue":
				warnings.add("unsupported Jinja loop control ignored during inspection render: " + stmt)
			case strings.HasPrefix(stmt, "end") || strings.HasPrefix(stmt, "elif ") || stmt == "else":
				warnings.add("malformed Jinja block marker ignored: " + compactJinjaSnippet(stmt))
			default:
				warnings.add("unsupported Jinja statement ignored: " + compactJinjaSnippet(stmt))
			}
		}
	}
	return out.String()
}

func findMatchingJinjaBlock(tokens []jinjaToken, start int, openTag string, closeTag string) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		if tokens[i].kind != "stmt" {
			continue
		}
		stmt := strings.TrimSpace(tokens[i].body)
		if strings.HasPrefix(stmt, openTag+" ") {
			depth++
			continue
		}
		if stmt == closeTag {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

var jinjaForRE = regexp.MustCompile(`^for\s+(.+?)\s+in\s+(.+)$`)

func renderJinjaFor(stmt string, body []jinjaToken, env map[string]any, warnings warningSet, budget *jinjaRenderBudget, depth int, out *strings.Builder) {
	match := jinjaForRE.FindStringSubmatch(stmt)
	if match == nil {
		warnings.add("unsupported Jinja for syntax: " + compactJinjaSnippet(stmt))
		return
	}
	names := splitJinjaCSV(match[1])
	items, ok := evalJinjaIterable(match[2], env, warnings)
	if !ok {
		warnings.add("unsupported Jinja iterable: " + compactJinjaSnippet(match[2]))
		items = []any{map[string]any{}}
	}
	if len(items) == 0 {
		return
	}
	for idx, item := range items {
		if !budget.consumeLoopIteration(warnings) {
			break
		}
		child := cloneJinjaEnv(env)
		assignJinjaLoopVars(child, names, item)
		child["loop"] = map[string]any{
			"index0": idx,
			"index":  idx + 1,
			"first":  idx == 0,
			"last":   idx == len(items)-1,
			"length": len(items),
		}
		out.WriteString(renderJinjaTokens(body, child, warnings, budget, depth))
	}
}

func assignJinjaLoopVars(env map[string]any, names []string, item any) {
	if len(names) <= 1 {
		if len(names) == 1 {
			env[strings.TrimSpace(names[0])] = item
		}
		return
	}
	if pair, ok := item.(map[string]any); ok {
		env[strings.TrimSpace(names[0])] = pair["key"]
		env[strings.TrimSpace(names[1])] = pair["value"]
		return
	}
	if values, ok := item.([]any); ok {
		for i, name := range names {
			if i < len(values) {
				env[strings.TrimSpace(name)] = values[i]
			}
		}
	}
}

type jinjaIfBranch struct {
	condition string
	body      []jinjaToken
}

func renderJinjaIf(firstStmt string, body []jinjaToken, env map[string]any, warnings warningSet, budget *jinjaRenderBudget, depth int) string {
	branches := []jinjaIfBranch{{condition: strings.TrimSpace(strings.TrimPrefix(firstStmt, "if ")), body: nil}}
	blockDepth := 0
	start := 0
	for i, token := range body {
		if token.kind != "stmt" {
			continue
		}
		stmt := strings.TrimSpace(token.body)
		switch {
		case strings.HasPrefix(stmt, "if "):
			blockDepth++
		case stmt == "endif":
			if blockDepth > 0 {
				blockDepth--
			}
		case blockDepth == 0 && strings.HasPrefix(stmt, "elif "):
			branches[len(branches)-1].body = body[start:i]
			branches = append(branches, jinjaIfBranch{condition: strings.TrimSpace(strings.TrimPrefix(stmt, "elif "))})
			start = i + 1
		case blockDepth == 0 && stmt == "else":
			branches[len(branches)-1].body = body[start:i]
			branches = append(branches, jinjaIfBranch{condition: ""})
			start = i + 1
		}
	}
	branches[len(branches)-1].body = body[start:]
	unknown := false
	for _, branch := range branches {
		if branch.condition == "" {
			if unknown {
				return renderJinjaTokens(branch.body, cloneJinjaEnv(env), warnings, budget, depth)
			}
			return renderJinjaTokens(branch.body, cloneJinjaEnv(env), warnings, budget, depth)
		}
		matched, ok := evalJinjaBool(branch.condition, env, warnings)
		if !ok {
			unknown = true
			warnings.add("unsupported Jinja if condition; rendering all branches for inspection: " + compactJinjaSnippet(branch.condition))
			var out strings.Builder
			for _, fallback := range branches {
				out.WriteString(renderJinjaTokens(fallback.body, cloneJinjaEnv(env), warnings, budget, depth))
			}
			return out.String()
		}
		if matched {
			return renderJinjaTokens(branch.body, cloneJinjaEnv(env), warnings, budget, depth)
		}
	}
	return ""
}

func applyJinjaSet(stmt string, env map[string]any, warnings warningSet) {
	assignment := strings.TrimSpace(strings.TrimPrefix(stmt, "set "))
	parts := splitJinjaTopLevelOperator(assignment, "=")
	if len(parts) != 2 {
		warnings.add("unsupported Jinja set syntax: " + compactJinjaSnippet(stmt))
		return
	}
	name := strings.TrimSpace(parts[0])
	value, ok := evalJinjaValue(parts[1], env, warnings)
	if !ok {
		warnings.add("unsupported Jinja set expression: " + compactJinjaSnippet(parts[1]))
		return
	}
	if dot := strings.Index(name, "."); dot > 0 {
		base, attr := name[:dot], name[dot+1:]
		if target, ok := env[base].(map[string]any); ok {
			target[attr] = value
		}
		return
	}
	env[name] = value
}

func evalJinjaIterable(expr string, env map[string]any, warnings warningSet) ([]any, bool) {
	expr = strings.TrimSpace(expr)
	if strings.HasSuffix(expr, ".items()") {
		value, ok := evalJinjaValue(strings.TrimSuffix(expr, ".items()"), env, warnings)
		if !ok {
			return nil, false
		}
		return jinjaItems(value), true
	}
	value, ok := evalJinjaValue(expr, env, warnings)
	if !ok {
		return nil, false
	}
	switch typed := value.(type) {
	case []any:
		return typed, true
	case map[string]any:
		return jinjaItems(typed), true
	case string:
		if typed == "" {
			return nil, true
		}
		return []any{typed}, true
	default:
		return nil, false
	}
}

func evalJinjaBool(expr string, env map[string]any, warnings warningSet) (bool, bool) {
	expr = trimJinjaOuterParens(strings.TrimSpace(expr))
	if expr == "" {
		return false, true
	}
	if strings.HasPrefix(expr, "not ") {
		value, ok := evalJinjaBool(strings.TrimSpace(strings.TrimPrefix(expr, "not ")), env, warnings)
		return !value, ok
	}
	if parts := splitJinjaTopLevelWord(expr, "or"); len(parts) > 1 {
		known := true
		for _, part := range parts {
			value, ok := evalJinjaBool(part, env, warnings)
			if ok && value {
				return true, true
			}
			known = known && ok
		}
		return false, known
	}
	if parts := splitJinjaTopLevelWord(expr, "and"); len(parts) > 1 {
		known := true
		for _, part := range parts {
			value, ok := evalJinjaBool(part, env, warnings)
			if ok && !value {
				return false, true
			}
			known = known && ok
		}
		return true, known
	}
	if parts := splitJinjaTopLevelWord(expr, "in"); len(parts) == 2 {
		needle, okNeedle := evalJinjaValue(parts[0], env, warnings)
		haystack, okHaystack := evalJinjaValue(parts[1], env, warnings)
		if !okNeedle || !okHaystack {
			return false, false
		}
		return strings.Contains(jinjaString(haystack), jinjaString(needle)), true
	}
	for _, op := range []string{"==", "!=", ">=", "<=", ">", "<"} {
		if parts := splitJinjaTopLevelOperator(expr, op); len(parts) == 2 {
			left, okLeft := evalJinjaValue(parts[0], env, warnings)
			right, okRight := evalJinjaValue(parts[1], env, warnings)
			if !okLeft || !okRight {
				return false, false
			}
			cmp := compareJinjaValues(left, right)
			switch op {
			case "==":
				return cmp == 0, true
			case "!=":
				return cmp != 0, true
			case ">":
				return cmp > 0, true
			case "<":
				return cmp < 0, true
			case ">=":
				return cmp >= 0, true
			case "<=":
				return cmp <= 0, true
			}
		}
	}
	if value, ok := evalJinjaTest(expr, env, warnings); ok {
		return value, true
	}
	value, ok := evalJinjaValue(expr, env, warnings)
	if !ok {
		return false, false
	}
	return jinjaTruthy(value), true
}

func evalJinjaTest(expr string, env map[string]any, warnings warningSet) (bool, bool) {
	for _, marker := range []string{" is not ", " is "} {
		if parts := splitJinjaTopLevelMarker(expr, marker); len(parts) == 2 {
			value, ok := evalJinjaValue(parts[0], env, warnings)
			if !ok && strings.TrimSpace(parts[1]) == "defined" {
				return marker == " is not ", true
			}
			if !ok {
				return false, false
			}
			result := false
			switch strings.TrimSpace(parts[1]) {
			case "defined":
				result = true
			case "undefined":
				result = false
			case "string":
				_, result = value.(string)
			case "mapping":
				_, result = value.(map[string]any)
			case "iterable", "sequence":
				_, result = value.([]any)
			case "none":
				result = value == nil
			case "false":
				boolValue, _ := value.(bool)
				result = !boolValue
			case "true":
				boolValue, _ := value.(bool)
				result = boolValue
			default:
				return false, false
			}
			if marker == " is not " {
				result = !result
			}
			return result, true
		}
	}
	return false, false
}

func evalJinjaValue(expr string, env map[string]any, warnings warningSet) (any, bool) {
	expr = trimJinjaOuterParens(strings.TrimSpace(expr))
	if expr == "" {
		return "", true
	}
	if cond := splitJinjaInlineConditional(expr); len(cond) == 3 {
		matched, ok := evalJinjaBool(cond[1], env, warnings)
		if !ok {
			return nil, false
		}
		if matched {
			return evalJinjaValue(cond[0], env, warnings)
		}
		return evalJinjaValue(cond[2], env, warnings)
	}
	if parts := splitJinjaTopLevelAny(expr, []string{"+", "~"}); len(parts) > 1 {
		var out strings.Builder
		for _, part := range parts {
			value, ok := evalJinjaValue(part, env, warnings)
			if !ok {
				return nil, false
			}
			out.WriteString(jinjaString(value))
		}
		return out.String(), true
	}
	if parts := splitJinjaTopLevelOperator(expr, "-"); len(parts) == 2 {
		left, okLeft := evalJinjaValue(parts[0], env, warnings)
		right, okRight := evalJinjaValue(parts[1], env, warnings)
		if okLeft && okRight {
			return jinjaInt(left) - jinjaInt(right), true
		}
	}
	if strings.HasPrefix(expr, "namespace(") && strings.HasSuffix(expr, ")") {
		out := map[string]any{}
		inner := strings.TrimSuffix(strings.TrimPrefix(expr, "namespace("), ")")
		for _, part := range splitJinjaCSV(inner) {
			pieces := splitJinjaTopLevelOperator(part, "=")
			if len(pieces) != 2 {
				continue
			}
			value, ok := evalJinjaValue(pieces[1], env, warnings)
			if ok {
				out[strings.TrimSpace(pieces[0])] = value
			}
		}
		return out, true
	}
	if strings.HasPrefix(expr, "range(") && strings.HasSuffix(expr, ")") {
		return evalJinjaRange(expr, env, warnings)
	}
	if value, ok := jinjaLiteral(expr); ok {
		return value, true
	}
	if strings.HasPrefix(expr, "[") && strings.HasSuffix(expr, "]") {
		items := []any{}
		for _, part := range splitJinjaCSV(strings.TrimSuffix(strings.TrimPrefix(expr, "["), "]")) {
			value, ok := evalJinjaValue(part, env, warnings)
			if !ok {
				return nil, false
			}
			items = append(items, value)
		}
		return items, true
	}
	if value, ok := evalJinjaFilteredValue(expr, env, warnings); ok {
		return value, true
	}
	if value, ok := evalJinjaMethodCall(expr, env, warnings); ok {
		return value, true
	}
	if value, ok := resolveJinjaPath(expr, env); ok {
		return value, true
	}
	if value, ok := evalJinjaFunctionFallback(expr, env, warnings); ok {
		return value, true
	}
	return nil, false
}

func evalJinjaFilteredValue(expr string, env map[string]any, warnings warningSet) (any, bool) {
	parts := splitJinjaTopLevelOperator(expr, "|")
	if len(parts) <= 1 {
		return nil, false
	}
	value, ok := evalJinjaValue(parts[0], env, warnings)
	if !ok {
		return nil, false
	}
	for _, rawFilter := range parts[1:] {
		filter := strings.TrimSpace(rawFilter)
		name := filter
		if at := strings.Index(name, "("); at >= 0 {
			name = strings.TrimSpace(name[:at])
		}
		switch name {
		case "tojson":
			buf, err := json.Marshal(value)
			if err != nil {
				return nil, false
			}
			value = string(buf)
		case "trim":
			value = strings.TrimSpace(jinjaString(value))
		case "string":
			value = jinjaString(value)
		case "safe":
			continue
		case "items":
			value = jinjaItems(value)
		case "length":
			value = jinjaLen(value)
		default:
			warnings.add("unsupported Jinja filter ignored: " + name)
		}
	}
	return value, true
}

func evalJinjaMethodCall(expr string, env map[string]any, warnings warningSet) (any, bool) {
	for _, method := range []string{".get(", ".strip(", ".lstrip(", ".rstrip(", ".startswith(", ".endswith(", ".split("} {
		idx := strings.LastIndex(expr, method)
		if idx < 0 || !strings.HasSuffix(expr, ")") {
			continue
		}
		receiver, ok := evalJinjaValue(expr[:idx], env, warnings)
		if !ok {
			return nil, false
		}
		args := splitJinjaCSV(strings.TrimSuffix(expr[idx+len(method):], ")"))
		methodName := strings.Trim(method, ".(")
		return applyJinjaMethod(receiver, methodName, args, env, warnings)
	}
	return nil, false
}

func applyJinjaMethod(receiver any, method string, args []string, env map[string]any, warnings warningSet) (any, bool) {
	switch method {
	case "get":
		key := ""
		if len(args) > 0 {
			value, ok := evalJinjaValue(args[0], env, warnings)
			if !ok {
				return nil, false
			}
			key = jinjaString(value)
		}
		if mapped, ok := receiver.(map[string]any); ok {
			if value, exists := mapped[key]; exists {
				return value, true
			}
		}
		if len(args) > 1 {
			return evalJinjaValue(args[1], env, warnings)
		}
		return nil, true
	case "strip":
		return strings.TrimSpace(jinjaString(receiver)), true
	case "lstrip":
		cutset := " \t\r\n"
		if len(args) > 0 {
			if value, ok := evalJinjaValue(args[0], env, warnings); ok {
				cutset = jinjaString(value)
			}
		}
		return strings.TrimLeft(jinjaString(receiver), cutset), true
	case "rstrip":
		cutset := " \t\r\n"
		if len(args) > 0 {
			if value, ok := evalJinjaValue(args[0], env, warnings); ok {
				cutset = jinjaString(value)
			}
		}
		return strings.TrimRight(jinjaString(receiver), cutset), true
	case "startswith":
		if len(args) == 0 {
			return false, true
		}
		value, ok := evalJinjaValue(args[0], env, warnings)
		return strings.HasPrefix(jinjaString(receiver), jinjaString(value)), ok
	case "endswith":
		if len(args) == 0 {
			return false, true
		}
		value, ok := evalJinjaValue(args[0], env, warnings)
		return strings.HasSuffix(jinjaString(receiver), jinjaString(value)), ok
	case "split":
		sep := " "
		if len(args) > 0 {
			value, ok := evalJinjaValue(args[0], env, warnings)
			if !ok {
				return nil, false
			}
			sep = jinjaString(value)
		}
		parts := strings.Split(jinjaString(receiver), sep)
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			out = append(out, part)
		}
		return out, true
	default:
		return nil, false
	}
}

func evalJinjaFunctionFallback(expr string, env map[string]any, warnings warningSet) (any, bool) {
	open := strings.Index(expr, "(")
	if open <= 0 || !strings.HasSuffix(expr, ")") {
		return nil, false
	}
	name := strings.TrimSpace(expr[:open])
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
		return nil, false
	}
	args := splitJinjaCSV(strings.TrimSuffix(expr[open+1:], ")"))
	if len(args) == 0 {
		return "", true
	}
	value, ok := evalJinjaValue(args[0], env, warnings)
	if ok {
		warnings.add("unsupported Jinja function call approximated with first argument: " + name)
		return value, true
	}
	return nil, false
}

func evalJinjaRange(expr string, env map[string]any, warnings warningSet) ([]any, bool) {
	inner := strings.TrimSuffix(strings.TrimPrefix(expr, "range("), ")")
	args := splitJinjaCSV(inner)
	nums := []int{}
	for _, arg := range args {
		value, ok := evalJinjaValue(arg, env, warnings)
		if !ok {
			return nil, false
		}
		nums = append(nums, jinjaInt(value))
	}
	start, stop, step := 0, 0, 1
	switch len(nums) {
	case 1:
		stop = nums[0]
	case 2:
		start, stop = nums[0], nums[1]
	case 3:
		start, stop, step = nums[0], nums[1], nums[2]
	default:
		return nil, false
	}
	if step == 0 {
		return nil, false
	}
	out := []any{}
	for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
		if len(out) >= 32 {
			break
		}
		out = append(out, i)
	}
	return out, true
}

func resolveJinjaPath(expr string, env map[string]any) (any, bool) {
	parts, ok := splitJinjaPath(expr)
	if !ok || len(parts) == 0 {
		return nil, false
	}
	value, ok := env[parts[0]]
	if !ok {
		return nil, false
	}
	for _, part := range parts[1:] {
		switch typed := value.(type) {
		case map[string]any:
			value, ok = typed[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			value = typed[index]
		default:
			return nil, false
		}
	}
	return value, true
}

func splitJinjaPath(expr string) ([]string, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, false
	}
	parts := []string{}
	for len(expr) > 0 {
		if len(parts) == 0 {
			i := 0
			for i < len(expr) && ((expr[i] >= 'A' && expr[i] <= 'Z') || (expr[i] >= 'a' && expr[i] <= 'z') || (expr[i] >= '0' && expr[i] <= '9') || expr[i] == '_') {
				i++
			}
			if i == 0 {
				return nil, false
			}
			parts = append(parts, expr[:i])
			expr = expr[i:]
			continue
		}
		switch {
		case strings.HasPrefix(expr, "."):
			expr = expr[1:]
			i := 0
			for i < len(expr) && ((expr[i] >= 'A' && expr[i] <= 'Z') || (expr[i] >= 'a' && expr[i] <= 'z') || (expr[i] >= '0' && expr[i] <= '9') || expr[i] == '_') {
				i++
			}
			if i == 0 {
				return nil, false
			}
			parts = append(parts, expr[:i])
			expr = expr[i:]
		case strings.HasPrefix(expr, "["):
			end := strings.Index(expr, "]")
			if end < 0 {
				return nil, false
			}
			key := strings.TrimSpace(expr[1:end])
			if lit, ok := jinjaLiteral(key); ok {
				key = jinjaString(lit)
			}
			parts = append(parts, key)
			expr = expr[end+1:]
		default:
			return nil, false
		}
	}
	return parts, true
}

func jinjaLiteral(expr string) (any, bool) {
	expr = strings.TrimSpace(expr)
	if len(expr) >= 2 && ((expr[0] == '\'' && expr[len(expr)-1] == '\'') || (expr[0] == '"' && expr[len(expr)-1] == '"')) {
		return unquoteJinjaString(expr), true
	}
	switch expr {
	case "true", "True":
		return true, true
	case "false", "False":
		return false, true
	case "none", "None", "null":
		return nil, true
	}
	if n, err := strconv.Atoi(expr); err == nil {
		return n, true
	}
	return nil, false
}

func unquoteJinjaString(value string) string {
	if len(value) < 2 {
		return value
	}
	quote := value[0]
	inner := value[1 : len(value)-1]
	if quote == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	replacer := strings.NewReplacer(`\\`, `\`, `\n`, "\n", `\r`, "\r", `\t`, "\t", `\'`, `'`, `\"`, `"`)
	return replacer.Replace(inner)
}

func splitJinjaInlineConditional(expr string) []string {
	parts := splitJinjaTopLevelWord(expr, "if")
	if len(parts) != 2 {
		return nil
	}
	elseParts := splitJinjaTopLevelWord(parts[1], "else")
	if len(elseParts) != 2 {
		return nil
	}
	return []string{parts[0], elseParts[0], elseParts[1]}
}

func splitJinjaCSV(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := splitJinjaTopLevelOperator(value, ",")
	if len(parts) == 0 {
		return []string{value}
	}
	return parts
}

func splitJinjaTopLevelAny(value string, operators []string) []string {
	parts := []string{}
	start := 0
	quote := byte(0)
	depth := 0
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if quote != 0 {
			if ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		switch ch {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		}
		if depth != 0 {
			continue
		}
		for _, op := range operators {
			if strings.HasPrefix(value[i:], op) {
				parts = append(parts, strings.TrimSpace(value[start:i]))
				i += len(op) - 1
				start = i + 1
				break
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func splitJinjaTopLevelOperator(value string, operator string) []string {
	if operator == "" {
		return nil
	}
	parts := []string{}
	start := 0
	quote := byte(0)
	depth := 0
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if quote != 0 {
			if ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		switch ch {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && strings.HasPrefix(value[i:], operator) {
			parts = append(parts, strings.TrimSpace(value[start:i]))
			i += len(operator) - 1
			start = i + 1
		}
	}
	if len(parts) == 0 {
		return nil
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func splitJinjaTopLevelWord(value string, word string) []string {
	return splitJinjaTopLevelMarker(value, " "+word+" ")
}

func splitJinjaTopLevelMarker(value string, marker string) []string {
	parts := []string{}
	start := 0
	quote := byte(0)
	depth := 0
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if quote != 0 {
			if ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		switch ch {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && strings.HasPrefix(value[i:], marker) {
			parts = append(parts, strings.TrimSpace(value[start:i]))
			i += len(marker) - 1
			start = i + 1
		}
	}
	if len(parts) == 0 {
		return nil
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func trimJinjaOuterParens(value string) string {
	for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
		depth := 0
		balanced := true
		for i := 0; i < len(value); i++ {
			switch value[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i != len(value)-1 {
					balanced = false
				}
			}
		}
		if !balanced {
			break
		}
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func inspectionEnvironments() []map[string]any {
	base := map[string]any{
		"bos_token":             "<s>",
		"eos_token":             "</s>",
		"add_generation_prompt": true,
		"enable_thinking":       true,
		"clear_thinking":        false,
		"add_vision_id":         true,
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "lookup",
					"description": "sample tool",
					"parameters":  map[string]any{"type": "object"},
				},
			},
		},
		"messages": []any{
			map[string]any{"role": "system", "content": "system prompt"},
			map[string]any{"role": "user", "content": "user prompt"},
			map[string]any{
				"role":              "assistant",
				"content":           "visible answer",
				"reasoning_content": "sample reasoning",
				"tool_calls": []any{
					map[string]any{
						"id": "call_1",
						"function": map[string]any{
							"name":      "lookup",
							"arguments": map[string]any{"query": "cats"},
						},
					},
				},
			},
			map[string]any{"role": "tool", "content": "tool result", "tool_call_id": "call_1"},
		},
	}
	withoutThinking := cloneJinjaEnv(base)
	withoutThinking["enable_thinking"] = false
	withoutThinking["tools"] = []any{}
	return []map[string]any{base, withoutThinking}
}

func cloneJinjaEnv(env map[string]any) map[string]any {
	out := make(map[string]any, len(env))
	for key, value := range env {
		out[key] = value
	}
	return out
}

func jinjaItems(value any) []any {
	mapped, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(mapped))
	for key := range mapped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"key": key, "value": mapped[key]})
	}
	return out
}

func jinjaString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func jinjaTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case int:
		return typed != 0
	default:
		return true
	}
}

func jinjaInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		n, _ := strconv.Atoi(typed)
		return n
	default:
		return 0
	}
}

func jinjaLen(value any) int {
	switch typed := value.(type) {
	case string:
		return len(typed)
	case []any:
		return len(typed)
	case map[string]any:
		return len(typed)
	default:
		return 0
	}
}

func compareJinjaValues(left any, right any) int {
	if _, ok := left.(int); ok {
		li, ri := jinjaInt(left), jinjaInt(right)
		switch {
		case li < ri:
			return -1
		case li > ri:
			return 1
		default:
			return 0
		}
	}
	ls, rs := jinjaString(left), jinjaString(right)
	switch {
	case ls < rs:
		return -1
	case ls > rs:
		return 1
	default:
		return 0
	}
}

func compactJinjaSnippet(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 120 {
		return value[:117] + "..."
	}
	return value
}

func collectTemplateEvidence(raw string, rendered string) []TemplateEvidence {
	seen := map[string]struct{}{}
	var out []TemplateEvidence
	add := func(kind, value, source, text string) {
		key := kind + "\x00" + value + "\x00" + source
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, TemplateEvidence{Kind: kind, Value: value, Source: source, Snippet: evidenceSnippet(text, value)})
	}
	collect := func(source, text string) {
		for _, tag := range []string{"think", "analysis", "reasoning", "thought"} {
			if strings.Contains(text, "<"+tag+">") || strings.Contains(text, "</"+tag+">") {
				add("think_tag", tag, source, text)
			}
		}
		if strings.Contains(text, "<|tool_call_begin|>") || strings.Contains(text, "<|tool_call_argument_begin|>") {
			add("tool_call_format", "k2_json", source, text)
		}
		if detectQwenXMLToolCallTemplate(text) {
			add("tool_call_format", "qwen_xml", source, text)
		}
		for _, tag := range []string{"tool_call", "function_call", "tool"} {
			if strings.Contains(text, "<"+tag+">") || strings.Contains(text, "</"+tag+">") {
				add("tool_tag", tag, source, text)
			}
		}
		for _, token := range collectSpecialTokenEvidence(text) {
			add("special_token", token, source, text)
		}
	}
	collect("raw", raw)
	if strings.TrimSpace(rendered) != "" {
		collect("rendered_inspection", rendered)
	}
	return out
}

var specialTokenEvidenceRE = regexp.MustCompile(`(<\|[^>\s]+\|>|\[gMASK\]|<s>|</s>)`)

func collectSpecialTokenEvidence(text string) []string {
	matches := specialTokenEvidenceRE.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, match := range matches {
		if _, exists := seen[match]; exists {
			continue
		}
		seen[match] = struct{}{}
		out = append(out, match)
		if len(out) >= 32 {
			break
		}
	}
	return out
}

func evidenceSnippet(text string, value string) string {
	if value == "" {
		return ""
	}
	idx := strings.Index(text, value)
	if idx < 0 {
		return ""
	}
	start := idx - 48
	if start < 0 {
		start = 0
	}
	end := idx + len(value) + 48
	if end > len(text) {
		end = len(text)
	}
	return compactJinjaSnippet(text[start:end])
}
