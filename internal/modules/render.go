package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const (
	RuntimeJinjaEngine = "bounded_jinja_runtime"

	defaultRuntimeJinjaMaxOutputBytes    = 256 << 10
	defaultRuntimeJinjaMaxRenderDepth    = 64
	defaultRuntimeJinjaMaxLoopIterations = 4096
)

var ErrRuntimeJinjaLimit = errors.New("runtime Jinja render limit reached")

type RuntimeJinjaRenderOptions struct {
	MaxOutputBytes     int
	MaxRenderDepth     int
	MaxLoopIterations  int
	PreserveWhitespace bool
}

type RuntimeJinjaRenderResult struct {
	Rendered  string   `json:"rendered"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated"`
}

type runtimeJinjaToken struct {
	kind string
	body string
	raw  string
}

type runtimeJinjaRenderer struct {
	opts           RuntimeJinjaRenderOptions
	warnings       warningSet
	outputBytes    int
	loopIterations int
	truncated      bool
}

type runtimeUndefined struct {
	name string
}

type runtimeLoopControl string

const (
	runtimeLoopBreak    runtimeLoopControl = "break"
	runtimeLoopContinue runtimeLoopControl = "continue"
)

func RenderJinjaTemplate(text string, env map[string]any, opts RuntimeJinjaRenderOptions) (RuntimeJinjaRenderResult, error) {
	renderer := &runtimeJinjaRenderer{
		opts:     normalizeRuntimeJinjaOptions(opts),
		warnings: warningSet{},
	}
	tokens, err := tokenizeRuntimeJinja(text, !renderer.opts.PreserveWhitespace)
	if err != nil {
		return RuntimeJinjaRenderResult{}, err
	}
	out, err := renderer.renderTokens(tokens, cloneJinjaEnv(env), 0)
	result := RuntimeJinjaRenderResult{
		Rendered:  out,
		Warnings:  renderer.warnings.sorted(),
		Truncated: renderer.truncated,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func DefaultRuntimeJinjaChatContext() map[string]any {
	return map[string]any{
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
					"description": "sample lookup tool",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a concise assistant."},
			map[string]any{"role": "user", "content": "Find cats."},
			map[string]any{
				"role":              "assistant",
				"content":           "I will look that up.",
				"reasoning_content": "Need a lookup.",
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
			map[string]any{"role": "tool", "content": "cats are mammals", "tool_call_id": "call_1"},
		},
	}
}

func normalizeRuntimeJinjaOptions(opts RuntimeJinjaRenderOptions) RuntimeJinjaRenderOptions {
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = defaultRuntimeJinjaMaxOutputBytes
	}
	if opts.MaxRenderDepth <= 0 {
		opts.MaxRenderDepth = defaultRuntimeJinjaMaxRenderDepth
	}
	if opts.MaxLoopIterations <= 0 {
		opts.MaxLoopIterations = defaultRuntimeJinjaMaxLoopIterations
	}
	return opts
}

func tokenizeRuntimeJinja(text string, trimWhitespace bool) ([]runtimeJinjaToken, error) {
	var tokens []runtimeJinjaToken
	for len(text) > 0 {
		idx, open := nextRuntimeJinjaOpen(text)
		if idx < 0 {
			if text != "" {
				tokens = append(tokens, runtimeJinjaToken{kind: "text", raw: text})
			}
			break
		}
		leftTrim := trimWhitespace && idx+len(open) < len(text) && text[idx+len(open)] == '-'
		if idx > 0 {
			rawText := text[:idx]
			if leftTrim {
				rawText = strings.TrimRight(rawText, " \t\r\n")
			}
			if rawText != "" {
				tokens = append(tokens, runtimeJinjaToken{kind: "text", raw: rawText})
			}
		} else if leftTrim && len(tokens) > 0 && tokens[len(tokens)-1].kind == "text" {
			tokens[len(tokens)-1].raw = strings.TrimRight(tokens[len(tokens)-1].raw, " \t\r\n")
		}

		close := runtimeJinjaClose(open)
		bodyStart := idx + len(open)
		if bodyStart < len(text) && text[bodyStart] == '-' {
			bodyStart++
		}
		closeRel := strings.Index(text[bodyStart:], close)
		if closeRel < 0 {
			return nil, fmt.Errorf("malformed Jinja marker: unterminated %s", open)
		}
		bodyEnd := bodyStart + closeRel
		rightTrimMarker := bodyEnd > bodyStart && text[bodyEnd-1] == '-'
		rightTrim := trimWhitespace && rightTrimMarker
		rawEnd := bodyEnd + len(close)
		body := text[bodyStart:bodyEnd]
		if rightTrimMarker {
			body = body[:len(body)-1]
		}
		kind := "expr"
		if open == "{%" {
			kind = "stmt"
		} else if open == "{#" {
			kind = "comment"
		}
		tokens = append(tokens, runtimeJinjaToken{
			kind: kind,
			body: strings.TrimSpace(body),
			raw:  text[idx:rawEnd],
		})
		text = text[rawEnd:]
		if rightTrim {
			text = strings.TrimLeft(text, " \t\r\n")
		}
	}
	return tokens, nil
}

func nextRuntimeJinjaOpen(text string) (int, string) {
	idx := -1
	open := ""
	for _, candidate := range []string{"{{", "{%", "{#"} {
		if at := strings.Index(text, candidate); at >= 0 && (idx < 0 || at < idx) {
			idx = at
			open = candidate
		}
	}
	return idx, open
}

func runtimeJinjaClose(open string) string {
	switch open {
	case "{{":
		return "}}"
	case "{%":
		return "%}"
	case "{#":
		return "#}"
	default:
		return ""
	}
}

func (r *runtimeJinjaRenderer) renderTokens(tokens []runtimeJinjaToken, env map[string]any, depth int) (string, error) {
	if depth > r.opts.MaxRenderDepth {
		return "", fmt.Errorf("%w: runtime Jinja render depth limit %d exceeded", ErrRuntimeJinjaLimit, r.opts.MaxRenderDepth)
	}
	var out strings.Builder
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		switch token.kind {
		case "text":
			if err := r.writeString(&out, token.raw); err != nil {
				return out.String(), err
			}
		case "comment":
			continue
		case "expr":
			value, err := r.evalValue(token.body, env)
			if err != nil {
				return out.String(), err
			}
			if err := r.writeString(&out, runtimeJinjaString(value)); err != nil {
				return out.String(), err
			}
		case "stmt":
			stmt := strings.TrimSpace(token.body)
			switch {
			case strings.HasPrefix(stmt, "for "):
				bodyEnd := findMatchingRuntimeJinjaBlock(tokens, i, "for", "endfor")
				if bodyEnd < 0 {
					return out.String(), fmt.Errorf("malformed Jinja for block: missing endfor")
				}
				rendered, err := r.renderFor(stmt, tokens[i+1:bodyEnd], env, depth+1)
				if rendered != "" {
					out.WriteString(rendered)
				}
				if err != nil {
					if control, ok := err.(runtimeLoopControl); ok && control == runtimeLoopContinue {
						i = bodyEnd
						continue
					}
					return out.String(), err
				}
				i = bodyEnd
			case strings.HasPrefix(stmt, "if "):
				bodyEnd := findMatchingRuntimeJinjaBlock(tokens, i, "if", "endif")
				if bodyEnd < 0 {
					return out.String(), fmt.Errorf("malformed Jinja if block: missing endif")
				}
				rendered, err := r.renderIf(stmt, tokens[i+1:bodyEnd], env, depth+1)
				if rendered != "" {
					out.WriteString(rendered)
				}
				if err != nil {
					return out.String(), err
				}
				i = bodyEnd
			case strings.HasPrefix(stmt, "set "):
				if err := r.applySet(stmt, env); err != nil {
					return out.String(), err
				}
			case stmt == "break":
				return out.String(), runtimeLoopBreak
			case stmt == "continue":
				return out.String(), runtimeLoopContinue
			case strings.HasPrefix(stmt, "elif ") || stmt == "else" || strings.HasPrefix(stmt, "end"):
				return out.String(), fmt.Errorf("malformed Jinja block marker: %s", compactJinjaSnippet(stmt))
			default:
				return out.String(), fmt.Errorf("unsupported Jinja statement: %s", compactJinjaSnippet(stmt))
			}
		}
	}
	return out.String(), nil
}

func (r *runtimeJinjaRenderer) writeString(out *strings.Builder, value string) error {
	if value == "" {
		return nil
	}
	remaining := r.opts.MaxOutputBytes - r.outputBytes
	if remaining <= 0 {
		r.truncated = true
		return fmt.Errorf("%w: runtime Jinja output byte limit %d exceeded", ErrRuntimeJinjaLimit, r.opts.MaxOutputBytes)
	}
	if len(value) > remaining {
		out.WriteString(value[:remaining])
		r.outputBytes += remaining
		r.truncated = true
		return fmt.Errorf("%w: runtime Jinja output byte limit %d exceeded", ErrRuntimeJinjaLimit, r.opts.MaxOutputBytes)
	}
	out.WriteString(value)
	r.outputBytes += len(value)
	return nil
}

func findMatchingRuntimeJinjaBlock(tokens []runtimeJinjaToken, start int, openTag string, closeTag string) int {
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

func (r *runtimeJinjaRenderer) renderFor(stmt string, body []runtimeJinjaToken, env map[string]any, depth int) (string, error) {
	match := jinjaForRE.FindStringSubmatch(stmt)
	if match == nil {
		return "", fmt.Errorf("unsupported Jinja for syntax: %s", compactJinjaSnippet(stmt))
	}
	names := splitJinjaCSV(match[1])
	items, err := r.evalIterable(match[2], env)
	if err != nil {
		return "", err
	}
	loopBody, elseBody := splitRuntimeForElse(body)
	if len(items) == 0 {
		return r.renderTokens(elseBody, cloneJinjaEnv(env), depth)
	}
	var out strings.Builder
	for idx, item := range items {
		if r.loopIterations >= r.opts.MaxLoopIterations {
			return out.String(), fmt.Errorf("%w: runtime Jinja loop iteration limit %d exceeded", ErrRuntimeJinjaLimit, r.opts.MaxLoopIterations)
		}
		r.loopIterations++
		child := cloneJinjaEnv(env)
		assignJinjaLoopVars(child, names, item)
		loop := map[string]any{
			"index0": idx,
			"index":  idx + 1,
			"first":  idx == 0,
			"last":   idx == len(items)-1,
			"length": len(items),
		}
		if idx > 0 {
			loop["previtem"] = items[idx-1]
		}
		if idx+1 < len(items) {
			loop["nextitem"] = items[idx+1]
		}
		child["loop"] = loop
		rendered, err := r.renderTokens(loopBody, child, depth)
		if rendered != "" {
			out.WriteString(rendered)
		}
		if err != nil {
			if control, ok := err.(runtimeLoopControl); ok {
				if control == runtimeLoopBreak {
					break
				}
				if control == runtimeLoopContinue {
					continue
				}
			}
			return out.String(), err
		}
	}
	return out.String(), nil
}

func splitRuntimeForElse(body []runtimeJinjaToken) ([]runtimeJinjaToken, []runtimeJinjaToken) {
	depth := 0
	for i, token := range body {
		if token.kind != "stmt" {
			continue
		}
		stmt := strings.TrimSpace(token.body)
		switch {
		case startsRuntimeBlock(stmt):
			depth++
		case endsRuntimeBlock(stmt):
			if depth > 0 {
				depth--
			}
		case depth == 0 && stmt == "else":
			return body[:i], body[i+1:]
		}
	}
	return body, nil
}

type runtimeIfBranch struct {
	condition string
	body      []runtimeJinjaToken
}

func (r *runtimeJinjaRenderer) renderIf(firstStmt string, body []runtimeJinjaToken, env map[string]any, depth int) (string, error) {
	branches := splitRuntimeIfBranches(firstStmt, body)
	for _, branch := range branches {
		matched := true
		if branch.condition != "" {
			value, err := r.evalBool(branch.condition, env)
			if err != nil {
				return "", err
			}
			matched = value
		}
		if matched {
			return r.renderTokens(branch.body, cloneJinjaEnv(env), depth)
		}
	}
	return "", nil
}

func splitRuntimeIfBranches(firstStmt string, body []runtimeJinjaToken) []runtimeIfBranch {
	branches := []runtimeIfBranch{{condition: strings.TrimSpace(strings.TrimPrefix(firstStmt, "if "))}}
	start := 0
	depth := 0
	for i, token := range body {
		if token.kind != "stmt" {
			continue
		}
		stmt := strings.TrimSpace(token.body)
		switch {
		case startsRuntimeBlock(stmt):
			depth++
		case endsRuntimeBlock(stmt):
			if depth > 0 {
				depth--
			}
		case depth == 0 && strings.HasPrefix(stmt, "elif "):
			branches[len(branches)-1].body = body[start:i]
			branches = append(branches, runtimeIfBranch{condition: strings.TrimSpace(strings.TrimPrefix(stmt, "elif "))})
			start = i + 1
		case depth == 0 && stmt == "else":
			branches[len(branches)-1].body = body[start:i]
			branches = append(branches, runtimeIfBranch{})
			start = i + 1
		}
	}
	branches[len(branches)-1].body = body[start:]
	return branches
}

func startsRuntimeBlock(stmt string) bool {
	return strings.HasPrefix(stmt, "if ") || strings.HasPrefix(stmt, "for ")
}

func endsRuntimeBlock(stmt string) bool {
	return stmt == "endif" || stmt == "endfor"
}

func (r *runtimeJinjaRenderer) applySet(stmt string, env map[string]any) error {
	assignment := strings.TrimSpace(strings.TrimPrefix(stmt, "set "))
	left, right, ok := splitRuntimeAssignment(assignment)
	if !ok {
		return fmt.Errorf("unsupported Jinja set syntax: %s", compactJinjaSnippet(stmt))
	}
	value, err := r.evalValue(right, env)
	if err != nil {
		return err
	}
	return assignRuntimeTarget(left, value, env)
}

func splitRuntimeAssignment(value string) (string, string, bool) {
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
		if depth == 0 && ch == '=' {
			if (i > 0 && strings.ContainsRune("=!<>", rune(value[i-1]))) || (i+1 < len(value) && value[i+1] == '=') {
				continue
			}
			return strings.TrimSpace(value[:i]), strings.TrimSpace(value[i+1:]), true
		}
	}
	return "", "", false
}

func assignRuntimeTarget(target string, value any, env map[string]any) error {
	target = strings.TrimSpace(target)
	if !strings.Contains(target, ".") && !strings.Contains(target, "[") {
		if !isRuntimeIdentifier(target) {
			return fmt.Errorf("unsupported Jinja set target: %s", compactJinjaSnippet(target))
		}
		env[target] = value
		return nil
	}
	parentExpr, key, ok := splitRuntimeAssignmentTarget(target)
	if !ok {
		return fmt.Errorf("unsupported Jinja set target: %s", compactJinjaSnippet(target))
	}
	parent, resolved, pathLike, err := resolveRuntimePath(parentExpr, env, nil)
	if err != nil {
		return err
	}
	if !pathLike || !resolved {
		return fmt.Errorf("unsupported Jinja set target: %s", compactJinjaSnippet(target))
	}
	if mapped, ok := parent.(map[string]any); ok {
		mapped[key] = value
		return nil
	}
	rv := reflect.ValueOf(parent)
	if rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String && rv.Type().Elem().Kind() == reflect.Interface {
		rv.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf(value))
		return nil
	}
	return fmt.Errorf("unsupported Jinja set target: %s", compactJinjaSnippet(target))
}

func splitRuntimeAssignmentTarget(target string) (string, string, bool) {
	target = strings.TrimSpace(target)
	if strings.HasSuffix(target, "]") {
		open := matchingRuntimeBracketOpen(target)
		if open > 0 {
			keyExpr := strings.TrimSpace(target[open+1 : len(target)-1])
			if key, ok := jinjaLiteral(keyExpr); ok {
				return target[:open], runtimeJinjaString(key), true
			}
		}
	}
	if dot := lastRuntimeTopLevelDot(target); dot > 0 {
		key := target[dot+1:]
		if isRuntimeIdentifier(key) {
			return target[:dot], key, true
		}
	}
	return "", "", false
}

func (r *runtimeJinjaRenderer) evalIterable(expr string, env map[string]any) ([]any, error) {
	value, err := r.evalValue(expr, env)
	if err != nil {
		return nil, err
	}
	if isRuntimeUndefined(value) {
		return nil, nil
	}
	if items, ok := runtimeAnySlice(value); ok {
		return items, nil
	}
	if runtimeIsMapping(value) {
		keys := runtimeMapKeys(value)
		out := make([]any, 0, len(keys))
		for _, key := range keys {
			out = append(out, key)
		}
		return out, nil
	}
	if s, ok := value.(string); ok {
		if s == "" {
			return nil, nil
		}
		return []any{s}, nil
	}
	return nil, fmt.Errorf("unsupported Jinja iterable: %s", compactJinjaSnippet(expr))
}

func (r *runtimeJinjaRenderer) evalBool(expr string, env map[string]any) (bool, error) {
	value, err := r.evalValue(expr, env)
	if err != nil {
		return false, err
	}
	return runtimeJinjaTruthy(value), nil
}

func (r *runtimeJinjaRenderer) evalValue(expr string, env map[string]any) (any, error) {
	expr = trimJinjaOuterParens(strings.TrimSpace(expr))
	if expr == "" {
		return "", nil
	}
	if cond := splitJinjaInlineConditional(expr); len(cond) == 3 {
		matched, err := r.evalBool(cond[1], env)
		if err != nil {
			return nil, err
		}
		if matched {
			return r.evalValue(cond[0], env)
		}
		return r.evalValue(cond[2], env)
	}
	if parts := splitJinjaTopLevelWord(expr, "or"); len(parts) > 1 {
		var last any
		for _, part := range parts {
			value, err := r.evalValue(part, env)
			if err != nil {
				return nil, err
			}
			last = value
			if runtimeJinjaTruthy(value) {
				return value, nil
			}
		}
		return last, nil
	}
	if parts := splitJinjaTopLevelWord(expr, "and"); len(parts) > 1 {
		var last any
		for _, part := range parts {
			value, err := r.evalValue(part, env)
			if err != nil {
				return nil, err
			}
			last = value
			if !runtimeJinjaTruthy(value) {
				return value, nil
			}
		}
		return last, nil
	}
	if strings.HasPrefix(expr, "not ") {
		value, err := r.evalBool(strings.TrimSpace(strings.TrimPrefix(expr, "not ")), env)
		if err != nil {
			return nil, err
		}
		return !value, nil
	}
	if strings.HasPrefix(expr, "not(") && strings.HasSuffix(expr, ")") {
		value, err := r.evalBool(strings.TrimSpace(expr[3:]), env)
		if err != nil {
			return nil, err
		}
		return !value, nil
	}
	if parts := splitJinjaTopLevelMarker(expr, " not in "); len(parts) == 2 {
		value, err := r.evalIn(parts[0], parts[1], env)
		return !value, err
	}
	if parts := splitJinjaTopLevelWord(expr, "in"); len(parts) == 2 {
		return r.evalIn(parts[0], parts[1], env)
	}
	for _, op := range []string{"==", "!=", ">=", "<=", ">", "<"} {
		if left, right, ok := splitRuntimeComparison(expr, op); ok {
			lv, err := r.evalValue(left, env)
			if err != nil {
				return nil, err
			}
			rv, err := r.evalValue(right, env)
			if err != nil {
				return nil, err
			}
			cmp := runtimeCompareValues(lv, rv)
			switch op {
			case "==":
				return runtimeEqualValues(lv, rv), nil
			case "!=":
				return !runtimeEqualValues(lv, rv), nil
			case ">":
				return cmp > 0, nil
			case "<":
				return cmp < 0, nil
			case ">=":
				return cmp >= 0, nil
			case "<=":
				return cmp <= 0, nil
			}
		}
	}
	if value, ok, err := r.evalTest(expr, env); ok || err != nil {
		return value, err
	}
	if left, op, right, ok := splitRuntimeBinary(expr, []string{"+", "-", "~"}); ok {
		lv, err := r.evalValue(left, env)
		if err != nil {
			return nil, err
		}
		rv, err := r.evalValue(right, env)
		if err != nil {
			return nil, err
		}
		return applyRuntimeBinary(lv, rv, op)
	}
	if left, op, right, ok := splitRuntimeBinary(expr, []string{"//", "*", "/", "%"}); ok {
		lv, err := r.evalValue(left, env)
		if err != nil {
			return nil, err
		}
		rv, err := r.evalValue(right, env)
		if err != nil {
			return nil, err
		}
		return applyRuntimeBinary(lv, rv, op)
	}
	if strings.HasPrefix(expr, "-") && len(expr) > 1 {
		value, err := r.evalValue(expr[1:], env)
		if err != nil {
			return nil, err
		}
		n, ok := runtimeNumber(value)
		if !ok {
			return nil, fmt.Errorf("unsupported Jinja unary numeric expression: %s", compactJinjaSnippet(expr))
		}
		if runtimeIntegerish(value) {
			return -int(n), nil
		}
		return -n, nil
	}
	if strings.HasPrefix(expr, "+") && len(expr) > 1 {
		return r.evalValue(expr[1:], env)
	}
	if value, ok := jinjaLiteral(expr); ok {
		return value, nil
	}
	if strings.HasPrefix(expr, "[") && strings.HasSuffix(expr, "]") {
		return r.evalListLiteral(expr, env)
	}
	if strings.HasPrefix(expr, "{") && strings.HasSuffix(expr, "}") {
		return r.evalMapLiteral(expr, env)
	}
	if value, ok, err := r.evalFilteredValue(expr, env); ok || err != nil {
		return value, err
	}
	if value, ok, err := r.evalMethodCall(expr, env); ok || err != nil {
		return value, err
	}
	if value, ok, err := r.evalFunctionCall(expr, env); ok || err != nil {
		return value, err
	}
	if value, resolved, pathLike, err := resolveRuntimePath(expr, env, r); err != nil || pathLike {
		if err != nil {
			return nil, err
		}
		if !resolved {
			return runtimeUndefined{name: expr}, nil
		}
		return value, nil
	}
	return nil, fmt.Errorf("unsupported Jinja expression: %s", compactJinjaSnippet(expr))
}

func (r *runtimeJinjaRenderer) evalIn(needleExpr string, haystackExpr string, env map[string]any) (bool, error) {
	needle, err := r.evalValue(needleExpr, env)
	if err != nil {
		return false, err
	}
	haystack, err := r.evalValue(haystackExpr, env)
	if err != nil {
		return false, err
	}
	if isRuntimeUndefined(haystack) {
		return false, nil
	}
	if s, ok := haystack.(string); ok {
		return strings.Contains(s, runtimeJinjaString(needle)), nil
	}
	if items, ok := runtimeAnySlice(haystack); ok {
		for _, item := range items {
			if runtimeEqualValues(needle, item) {
				return true, nil
			}
		}
		return false, nil
	}
	if runtimeIsMapping(haystack) {
		_, ok := runtimeMapLookup(haystack, runtimeJinjaString(needle))
		return ok, nil
	}
	return false, nil
}

func splitRuntimeComparison(expr string, op string) (string, string, bool) {
	left, foundOp, right, ok := splitRuntimeBinary(expr, []string{op})
	if !ok || foundOp != op {
		return "", "", false
	}
	return left, right, true
}

func (r *runtimeJinjaRenderer) evalTest(expr string, env map[string]any) (bool, bool, error) {
	for _, marker := range []string{" is not ", " is "} {
		if parts := splitJinjaTopLevelMarker(expr, marker); len(parts) == 2 {
			value, err := r.evalValue(parts[0], env)
			if err != nil {
				return false, true, err
			}
			test := strings.TrimSpace(parts[1])
			result := false
			switch test {
			case "defined":
				result = !isRuntimeUndefined(value)
			case "undefined":
				result = isRuntimeUndefined(value)
			case "string":
				_, result = value.(string)
			case "mapping":
				result = runtimeIsMapping(value)
			case "iterable", "sequence":
				_, result = runtimeAnySlice(value)
			case "none":
				result = value == nil || isRuntimeUndefined(value)
			case "false":
				boolValue, _ := value.(bool)
				result = !boolValue
			case "true":
				boolValue, _ := value.(bool)
				result = boolValue
			default:
				return false, true, fmt.Errorf("unsupported Jinja test: %s", compactJinjaSnippet(test))
			}
			if marker == " is not " {
				result = !result
			}
			return result, true, nil
		}
	}
	return false, false, nil
}

func splitRuntimeBinary(value string, operators []string) (string, string, string, bool) {
	quote := byte(0)
	depth := 0
	bestIndex := -1
	bestOp := ""
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
			if strings.HasPrefix(value[i:], op) && runtimeBinaryAllowed(value, i, op) {
				bestIndex = i
				bestOp = op
				i += len(op) - 1
				break
			}
		}
	}
	if bestIndex < 0 {
		return "", "", "", false
	}
	left := strings.TrimSpace(value[:bestIndex])
	right := strings.TrimSpace(value[bestIndex+len(bestOp):])
	if left == "" || right == "" {
		return "", "", "", false
	}
	return left, bestOp, right, true
}

func runtimeBinaryAllowed(value string, index int, op string) bool {
	if op == "+" || op == "-" {
		prev := previousNonSpaceByte(value, index)
		if prev == 0 || strings.ContainsRune("+-*/%~(<>=!,[{:?&|", rune(prev)) {
			return false
		}
	}
	if op == "/" && index+1 < len(value) && value[index+1] == '/' {
		return false
	}
	if op == ">" || op == "<" {
		if index+1 < len(value) && value[index+1] == '=' {
			return false
		}
	}
	return true
}

func previousNonSpaceByte(value string, index int) byte {
	for i := index - 1; i >= 0; i-- {
		switch value[i] {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return value[i]
		}
	}
	return 0
}

func applyRuntimeBinary(left any, right any, op string) (any, error) {
	switch op {
	case "~":
		return runtimeJinjaString(left) + runtimeJinjaString(right), nil
	case "+":
		ln, lok := runtimeNumber(left)
		rn, rok := runtimeNumber(right)
		if lok && rok {
			if runtimeIntegerish(left) && runtimeIntegerish(right) {
				return int(ln) + int(rn), nil
			}
			return ln + rn, nil
		}
		return runtimeJinjaString(left) + runtimeJinjaString(right), nil
	case "-":
		ln, lok := runtimeNumber(left)
		rn, rok := runtimeNumber(right)
		if !lok || !rok {
			return nil, fmt.Errorf("unsupported Jinja numeric subtraction")
		}
		if runtimeIntegerish(left) && runtimeIntegerish(right) {
			return int(ln) - int(rn), nil
		}
		return ln - rn, nil
	case "*", "/", "//", "%":
		ln, lok := runtimeNumber(left)
		rn, rok := runtimeNumber(right)
		if !lok || !rok {
			return nil, fmt.Errorf("unsupported Jinja numeric operation %s", op)
		}
		if rn == 0 && (op == "/" || op == "//" || op == "%") {
			return nil, fmt.Errorf("runtime Jinja division by zero")
		}
		switch op {
		case "*":
			if runtimeIntegerish(left) && runtimeIntegerish(right) {
				return int(ln) * int(rn), nil
			}
			return ln * rn, nil
		case "/":
			return ln / rn, nil
		case "//":
			return int(math.Floor(ln / rn)), nil
		case "%":
			return int(ln) % int(rn), nil
		}
	}
	return nil, fmt.Errorf("unsupported Jinja binary operator: %s", op)
}

func (r *runtimeJinjaRenderer) evalListLiteral(expr string, env map[string]any) ([]any, error) {
	inner := strings.TrimSpace(expr[1 : len(expr)-1])
	if inner == "" {
		return []any{}, nil
	}
	parts := splitJinjaCSV(inner)
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		value, err := r.evalValue(part, env)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func (r *runtimeJinjaRenderer) evalMapLiteral(expr string, env map[string]any) (map[string]any, error) {
	inner := strings.TrimSpace(expr[1 : len(expr)-1])
	out := map[string]any{}
	if inner == "" {
		return out, nil
	}
	for _, part := range splitJinjaCSV(inner) {
		left, _, right, ok := splitRuntimeBinary(part, []string{":"})
		if !ok {
			return nil, fmt.Errorf("unsupported Jinja map literal item: %s", compactJinjaSnippet(part))
		}
		key, err := r.evalValue(left, env)
		if err != nil {
			return nil, err
		}
		value, err := r.evalValue(right, env)
		if err != nil {
			return nil, err
		}
		out[runtimeJinjaString(key)] = value
	}
	return out, nil
}

func (r *runtimeJinjaRenderer) evalFilteredValue(expr string, env map[string]any) (any, bool, error) {
	parts := splitJinjaTopLevelOperator(expr, "|")
	if len(parts) <= 1 {
		return nil, false, nil
	}
	value, err := r.evalValue(parts[0], env)
	if err != nil {
		return nil, true, err
	}
	for _, rawFilter := range parts[1:] {
		name, args, err := parseRuntimeCall(rawFilter)
		if err != nil {
			return nil, true, err
		}
		value, err = r.applyFilter(value, name, args, env)
		if err != nil {
			return nil, true, err
		}
	}
	return value, true, nil
}

func parseRuntimeCall(expr string) (string, []string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", nil, fmt.Errorf("unsupported empty Jinja call")
	}
	if !strings.Contains(expr, "(") {
		if !isRuntimeIdentifier(expr) {
			return "", nil, fmt.Errorf("unsupported Jinja call syntax: %s", compactJinjaSnippet(expr))
		}
		return expr, nil, nil
	}
	open := strings.Index(expr, "(")
	if open <= 0 || !strings.HasSuffix(expr, ")") {
		return "", nil, fmt.Errorf("unsupported Jinja call syntax: %s", compactJinjaSnippet(expr))
	}
	name := strings.TrimSpace(expr[:open])
	if !isRuntimeIdentifier(name) {
		return "", nil, fmt.Errorf("unsupported Jinja call syntax: %s", compactJinjaSnippet(expr))
	}
	return name, splitJinjaCSV(strings.TrimSpace(expr[open+1 : len(expr)-1])), nil
}

func (r *runtimeJinjaRenderer) applyFilter(value any, name string, args []string, env map[string]any) (any, error) {
	switch name {
	case "tojson":
		buf, err := json.Marshal(runtimeDefinedValue(value))
		if err != nil {
			return nil, err
		}
		return string(buf), nil
	case "default", "d":
		defaultValue := any("")
		if len(args) > 0 {
			valueArg, err := r.evalValue(args[0], env)
			if err != nil {
				return nil, err
			}
			defaultValue = valueArg
		}
		useFalsey := false
		if len(args) > 1 {
			flag, err := r.evalBool(args[1], env)
			if err != nil {
				return nil, err
			}
			useFalsey = flag
		}
		if isRuntimeUndefined(value) || (useFalsey && !runtimeJinjaTruthy(value)) {
			return defaultValue, nil
		}
		return value, nil
	case "trim":
		return strings.TrimSpace(runtimeJinjaString(value)), nil
	case "replace":
		if len(args) < 2 || len(args) > 3 {
			return nil, fmt.Errorf("Jinja replace filter expects two or three arguments")
		}
		oldValue, err := r.evalValue(args[0], env)
		if err != nil {
			return nil, err
		}
		newValue, err := r.evalValue(args[1], env)
		if err != nil {
			return nil, err
		}
		count := -1
		if len(args) == 3 {
			countValue, err := r.evalValue(args[2], env)
			if err != nil {
				return nil, err
			}
			count = runtimeJinjaInt(countValue)
		}
		return strings.Replace(runtimeJinjaString(value), runtimeJinjaString(oldValue), runtimeJinjaString(newValue), count), nil
	case "upper":
		return strings.ToUpper(runtimeJinjaString(value)), nil
	case "lower":
		return strings.ToLower(runtimeJinjaString(value)), nil
	case "length", "count":
		return runtimeJinjaLen(value), nil
	case "slice":
		return r.applySliceFilter(value, args, env)
	case "string":
		return runtimeJinjaString(value), nil
	case "safe":
		return value, nil
	case "items":
		return runtimeItems(value), nil
	case "join":
		sep := ""
		if len(args) > 0 {
			sepValue, err := r.evalValue(args[0], env)
			if err != nil {
				return nil, err
			}
			sep = runtimeJinjaString(sepValue)
		}
		items, ok := runtimeAnySlice(value)
		if !ok {
			return runtimeJinjaString(value), nil
		}
		parts := make([]string, 0, len(items))
		for _, item := range items {
			parts = append(parts, runtimeJinjaString(item))
		}
		return strings.Join(parts, sep), nil
	case "first":
		items, ok := runtimeAnySlice(value)
		if ok && len(items) > 0 {
			return items[0], nil
		}
		s := runtimeJinjaString(value)
		if s == "" {
			return runtimeUndefined{name: "first"}, nil
		}
		return s[:1], nil
	case "last":
		items, ok := runtimeAnySlice(value)
		if ok && len(items) > 0 {
			return items[len(items)-1], nil
		}
		s := runtimeJinjaString(value)
		if s == "" {
			return runtimeUndefined{name: "last"}, nil
		}
		return s[len(s)-1:], nil
	default:
		return nil, fmt.Errorf("unsupported Jinja filter: %s", name)
	}
}

func (r *runtimeJinjaRenderer) applySliceFilter(value any, args []string, env map[string]any) (any, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, fmt.Errorf("Jinja slice filter expects one or two arguments")
	}
	startValue, err := r.evalValue(args[0], env)
	if err != nil {
		return nil, err
	}
	start := runtimeJinjaInt(startValue)
	endSet := len(args) == 2
	end := 0
	if endSet {
		endValue, err := r.evalValue(args[1], env)
		if err != nil {
			return nil, err
		}
		end = runtimeJinjaInt(endValue)
	}
	if items, ok := runtimeAnySlice(value); ok {
		if !endSet {
			end = start
			start = 0
		}
		start, end = clampRuntimeSlice(len(items), start, end)
		return items[start:end], nil
	}
	s := runtimeJinjaString(value)
	if !endSet {
		end = start
		start = 0
	}
	start, end = clampRuntimeSlice(len(s), start, end)
	return s[start:end], nil
}

func clampRuntimeSlice(length int, start int, end int) (int, int) {
	if start < 0 {
		start = length + start
	}
	if end < 0 {
		end = length + end
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if start > length {
		start = length
	}
	if end > length {
		end = length
	}
	return start, end
}

func (r *runtimeJinjaRenderer) evalMethodCall(expr string, env map[string]any) (any, bool, error) {
	receiverExpr, name, args, ok := splitRuntimeMethodCall(expr)
	if !ok {
		return nil, false, nil
	}
	receiver, err := r.evalValue(receiverExpr, env)
	if err != nil {
		return nil, true, err
	}
	switch name {
	case "get":
		key := ""
		if len(args) > 0 {
			value, err := r.evalValue(args[0], env)
			if err != nil {
				return nil, true, err
			}
			key = runtimeJinjaString(value)
		}
		if value, ok := runtimeMapLookup(receiver, key); ok {
			return value, true, nil
		}
		if len(args) > 1 {
			value, err := r.evalValue(args[1], env)
			return value, true, err
		}
		return runtimeUndefined{name: key}, true, nil
	case "items":
		return runtimeItems(receiver), true, nil
	case "keys":
		keys := runtimeMapKeys(receiver)
		out := make([]any, 0, len(keys))
		for _, key := range keys {
			out = append(out, key)
		}
		return out, true, nil
	case "values":
		keys := runtimeMapKeys(receiver)
		out := make([]any, 0, len(keys))
		for _, key := range keys {
			value, _ := runtimeMapLookup(receiver, key)
			out = append(out, value)
		}
		return out, true, nil
	case "strip":
		return strings.TrimSpace(runtimeJinjaString(receiver)), true, nil
	case "lstrip":
		cutset, err := r.methodCutset(args, env)
		return strings.TrimLeft(runtimeJinjaString(receiver), cutset), true, err
	case "rstrip":
		cutset, err := r.methodCutset(args, env)
		return strings.TrimRight(runtimeJinjaString(receiver), cutset), true, err
	case "lower":
		return strings.ToLower(runtimeJinjaString(receiver)), true, nil
	case "upper":
		return strings.ToUpper(runtimeJinjaString(receiver)), true, nil
	case "replace":
		value, err := r.applyFilter(receiver, "replace", args, env)
		return value, true, err
	case "startswith":
		if len(args) == 0 {
			return false, true, nil
		}
		value, err := r.evalValue(args[0], env)
		if err != nil {
			return nil, true, err
		}
		return strings.HasPrefix(runtimeJinjaString(receiver), runtimeJinjaString(value)), true, nil
	case "endswith":
		if len(args) == 0 {
			return false, true, nil
		}
		value, err := r.evalValue(args[0], env)
		if err != nil {
			return nil, true, err
		}
		return strings.HasSuffix(runtimeJinjaString(receiver), runtimeJinjaString(value)), true, nil
	case "split":
		sep := " "
		if len(args) > 0 {
			value, err := r.evalValue(args[0], env)
			if err != nil {
				return nil, true, err
			}
			sep = runtimeJinjaString(value)
		}
		parts := strings.Split(runtimeJinjaString(receiver), sep)
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			out = append(out, part)
		}
		return out, true, nil
	default:
		return nil, true, fmt.Errorf("unsupported Jinja method: %s", name)
	}
}

func (r *runtimeJinjaRenderer) methodCutset(args []string, env map[string]any) (string, error) {
	cutset := " \t\r\n"
	if len(args) == 0 {
		return cutset, nil
	}
	value, err := r.evalValue(args[0], env)
	if err != nil {
		return "", err
	}
	return runtimeJinjaString(value), nil
}

func splitRuntimeMethodCall(expr string) (string, string, []string, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasSuffix(expr, ")") {
		return "", "", nil, false
	}
	open := matchingRuntimeCallOpen(expr)
	if open <= 0 {
		return "", "", nil, false
	}
	prefix := strings.TrimSpace(expr[:open])
	dot := lastRuntimeTopLevelDot(prefix)
	if dot <= 0 {
		return "", "", nil, false
	}
	name := strings.TrimSpace(prefix[dot+1:])
	if !isRuntimeIdentifier(name) {
		return "", "", nil, false
	}
	return strings.TrimSpace(prefix[:dot]), name, splitJinjaCSV(strings.TrimSpace(expr[open+1 : len(expr)-1])), true
}

func matchingRuntimeCallOpen(expr string) int {
	depth := 0
	quote := byte(0)
	for i := len(expr) - 1; i >= 0; i-- {
		ch := expr[i]
		if quote != 0 {
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
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func lastRuntimeTopLevelDot(expr string) int {
	quote := byte(0)
	depth := 0
	last := -1
	for i := 0; i < len(expr); i++ {
		ch := expr[i]
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
		case '.':
			if depth == 0 {
				last = i
			}
		}
	}
	return last
}

func matchingRuntimeBracketOpen(expr string) int {
	depth := 0
	quote := byte(0)
	for i := len(expr) - 1; i >= 0; i-- {
		ch := expr[i]
		if quote != 0 {
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
		case ']':
			depth++
		case '[':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func (r *runtimeJinjaRenderer) evalFunctionCall(expr string, env map[string]any) (any, bool, error) {
	name, args, ok := splitRuntimeFunctionCall(expr)
	if !ok {
		return nil, false, nil
	}
	switch name {
	case "range":
		value, err := r.evalRange(args, env)
		return value, true, err
	case "namespace":
		out := map[string]any{}
		for _, arg := range args {
			key, valueExpr, ok := splitRuntimeAssignment(arg)
			if !ok {
				return nil, true, fmt.Errorf("unsupported namespace argument: %s", compactJinjaSnippet(arg))
			}
			value, err := r.evalValue(valueExpr, env)
			if err != nil {
				return nil, true, err
			}
			out[strings.TrimSpace(key)] = value
		}
		return out, true, nil
	case "dict":
		out := map[string]any{}
		for _, arg := range args {
			key, valueExpr, ok := splitRuntimeAssignment(arg)
			if !ok {
				return nil, true, fmt.Errorf("unsupported dict argument: %s", compactJinjaSnippet(arg))
			}
			value, err := r.evalValue(valueExpr, env)
			if err != nil {
				return nil, true, err
			}
			out[strings.TrimSpace(key)] = value
		}
		return out, true, nil
	case "raise_exception":
		message := "template raised an exception"
		if len(args) > 0 {
			value, err := r.evalValue(args[0], env)
			if err != nil {
				return nil, true, err
			}
			message = runtimeJinjaString(value)
		}
		return nil, true, fmt.Errorf("runtime Jinja raise_exception: %s", message)
	default:
		return nil, true, fmt.Errorf("unsupported Jinja function: %s", name)
	}
}

func splitRuntimeFunctionCall(expr string) (string, []string, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasSuffix(expr, ")") {
		return "", nil, false
	}
	open := matchingRuntimeCallOpen(expr)
	if open <= 0 {
		return "", nil, false
	}
	name := strings.TrimSpace(expr[:open])
	if !isRuntimeIdentifier(name) {
		return "", nil, false
	}
	return name, splitJinjaCSV(strings.TrimSpace(expr[open+1 : len(expr)-1])), true
}

func (r *runtimeJinjaRenderer) evalRange(args []string, env map[string]any) ([]any, error) {
	nums := []int{}
	for _, arg := range args {
		value, err := r.evalValue(arg, env)
		if err != nil {
			return nil, err
		}
		nums = append(nums, runtimeJinjaInt(value))
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
		return nil, fmt.Errorf("range expects one to three arguments")
	}
	if step == 0 {
		return nil, fmt.Errorf("range step must not be zero")
	}
	out := []any{}
	for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
		if len(out) >= r.opts.MaxLoopIterations {
			return nil, fmt.Errorf("%w: runtime Jinja range limit %d exceeded", ErrRuntimeJinjaLimit, r.opts.MaxLoopIterations)
		}
		out = append(out, i)
	}
	return out, nil
}

func resolveRuntimePath(expr string, env map[string]any, renderer *runtimeJinjaRenderer) (any, bool, bool, error) {
	expr = strings.TrimSpace(expr)
	name, rest, ok := consumeRuntimeIdentifier(expr)
	if !ok {
		return nil, false, false, nil
	}
	value, resolved := env[name]
	if !resolved {
		value = runtimeUndefined{name: name}
	}
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "."):
			var attr string
			attr, rest, ok = consumeRuntimeIdentifier(rest[1:])
			if !ok {
				return nil, false, false, nil
			}
			if isRuntimeUndefined(value) {
				continue
			}
			next, exists := runtimeMapLookup(value, attr)
			if !exists {
				value = runtimeUndefined{name: attr}
				continue
			}
			value = next
		case strings.HasPrefix(rest, "["):
			close := findRuntimeBracketClose(rest)
			if close < 0 {
				return nil, false, false, nil
			}
			inner := strings.TrimSpace(rest[1:close])
			rest = rest[close+1:]
			if isRuntimeUndefined(value) {
				continue
			}
			if strings.Contains(inner, ":") {
				if renderer == nil {
					return nil, false, true, fmt.Errorf("runtime Jinja slice access requires renderer")
				}
				sliced, err := renderer.applyBracketSlice(value, inner, env)
				if err != nil {
					return nil, false, true, err
				}
				value = sliced
				continue
			}
			if renderer == nil {
				if lit, ok := jinjaLiteral(inner); ok {
					value, resolved = runtimeLookupIndex(value, lit)
				} else {
					value, resolved = runtimeLookupIndex(value, inner)
				}
			} else {
				key, err := renderer.evalValue(inner, env)
				if err != nil {
					return nil, false, true, err
				}
				value, resolved = runtimeLookupIndex(value, key)
			}
			if !resolved {
				value = runtimeUndefined{name: inner}
			}
		default:
			return nil, false, false, nil
		}
	}
	return value, !isRuntimeUndefined(value), true, nil
}

func consumeRuntimeIdentifier(value string) (string, string, bool) {
	if value == "" || !isRuntimeIdentifierStart(value[0]) {
		return "", "", false
	}
	i := 1
	for i < len(value) && isRuntimeIdentifierPart(value[i]) {
		i++
	}
	return value[:i], value[i:], true
}

func isRuntimeIdentifier(value string) bool {
	name, rest, ok := consumeRuntimeIdentifier(value)
	return ok && name != "" && rest == ""
}

func isRuntimeIdentifierStart(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_'
}

func isRuntimeIdentifierPart(ch byte) bool {
	return isRuntimeIdentifierStart(ch) || (ch >= '0' && ch <= '9')
}

func findRuntimeBracketClose(value string) int {
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
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func runtimeLookupIndex(value any, key any) (any, bool) {
	if isRuntimeUndefined(value) {
		return value, false
	}
	if s, ok := key.(string); ok {
		if mappedValue, exists := runtimeMapLookup(value, s); exists {
			return mappedValue, true
		}
	}
	index := runtimeJinjaInt(key)
	if items, ok := runtimeAnySlice(value); ok {
		if index < 0 {
			index = len(items) + index
		}
		if index >= 0 && index < len(items) {
			return items[index], true
		}
		return nil, false
	}
	if s, ok := value.(string); ok {
		if index < 0 {
			index = len(s) + index
		}
		if index >= 0 && index < len(s) {
			return s[index : index+1], true
		}
	}
	return nil, false
}

func (r *runtimeJinjaRenderer) applyBracketSlice(value any, inner string, env map[string]any) (any, error) {
	parts := splitJinjaTopLevelOperator(inner, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, fmt.Errorf("unsupported Jinja slice syntax: %s", compactJinjaSnippet(inner))
	}
	evalPart := func(part string, fallback int) (int, error) {
		if strings.TrimSpace(part) == "" {
			return fallback, nil
		}
		value, err := r.evalValue(part, env)
		if err != nil {
			return 0, err
		}
		return runtimeJinjaInt(value), nil
	}
	length := runtimeJinjaLen(value)
	step := 1
	if len(parts) == 3 {
		var err error
		step, err = evalPart(parts[2], 1)
		if err != nil {
			return nil, err
		}
	}
	if step == 0 {
		return nil, fmt.Errorf("Jinja slice step must not be zero")
	}
	startDefault, stopDefault := 0, length
	if step < 0 {
		startDefault, stopDefault = length-1, -length-1
	}
	start, err := evalPart(parts[0], startDefault)
	if err != nil {
		return nil, err
	}
	stop, err := evalPart(parts[1], stopDefault)
	if err != nil {
		return nil, err
	}
	if items, ok := runtimeAnySlice(value); ok {
		out := []any{}
		for _, idx := range runtimeSliceIndexes(len(items), start, stop, step) {
			out = append(out, items[idx])
		}
		return out, nil
	}
	s := runtimeJinjaString(value)
	var out strings.Builder
	for _, idx := range runtimeSliceIndexes(len(s), start, stop, step) {
		out.WriteByte(s[idx])
	}
	return out.String(), nil
}

func runtimeSliceIndexes(length int, start int, stop int, step int) []int {
	if start < 0 {
		start = length + start
	}
	if stop < 0 {
		stop = length + stop
	}
	if step > 0 {
		if start < 0 {
			start = 0
		}
		if start > length {
			start = length
		}
		if stop < 0 {
			stop = 0
		}
		if stop > length {
			stop = length
		}
	} else {
		if start < 0 {
			start = length - 1
		}
		if start >= length {
			start = length - 1
		}
		if stop < -1 {
			stop = -1
		}
		if stop >= length {
			stop = length - 1
		}
	}
	out := []int{}
	if step > 0 {
		for i := start; i < stop; i += step {
			if i >= 0 && i < length {
				out = append(out, i)
			}
		}
		return out
	}
	for i := start; i > stop; i += step {
		if i >= 0 && i < length {
			out = append(out, i)
		}
	}
	return out
}

func runtimeJinjaString(value any) string {
	if isRuntimeUndefined(value) {
		return ""
	}
	return jinjaString(value)
}

func runtimeDefinedValue(value any) any {
	if isRuntimeUndefined(value) {
		return nil
	}
	return value
}

func isRuntimeUndefined(value any) bool {
	_, ok := value.(runtimeUndefined)
	return ok
}

func runtimeJinjaTruthy(value any) bool {
	if isRuntimeUndefined(value) {
		return false
	}
	if number, ok := runtimeNumber(value); ok {
		return number != 0
	}
	if items, ok := runtimeAnySlice(value); ok {
		return len(items) > 0
	}
	if runtimeIsMapping(value) {
		return len(runtimeMapKeys(value)) > 0
	}
	return jinjaTruthy(value)
}

func runtimeJinjaInt(value any) int {
	if n, ok := runtimeNumber(value); ok {
		return int(n)
	}
	return 0
}

func runtimeJinjaLen(value any) int {
	if isRuntimeUndefined(value) {
		return 0
	}
	if s, ok := value.(string); ok {
		return len(s)
	}
	if items, ok := runtimeAnySlice(value); ok {
		return len(items)
	}
	if runtimeIsMapping(value) {
		return len(runtimeMapKeys(value))
	}
	return jinjaLen(value)
}

func runtimeNumber(value any) (float64, bool) {
	if isRuntimeUndefined(value) || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		if n, err := typed.Float64(); err == nil {
			return n, true
		}
	case string:
		if n, err := strconv.ParseFloat(typed, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func runtimeIntegerish(value any) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

func runtimeEqualValues(left any, right any) bool {
	if isRuntimeUndefined(left) || isRuntimeUndefined(right) {
		return isRuntimeUndefined(left) && isRuntimeUndefined(right)
	}
	if ln, lok := runtimeNumber(left); lok {
		if rn, rok := runtimeNumber(right); rok {
			return ln == rn
		}
	}
	return reflect.DeepEqual(left, right)
}

func runtimeCompareValues(left any, right any) int {
	if ln, lok := runtimeNumber(left); lok {
		if rn, rok := runtimeNumber(right); rok {
			switch {
			case ln < rn:
				return -1
			case ln > rn:
				return 1
			default:
				return 0
			}
		}
	}
	return compareJinjaValues(runtimeJinjaString(left), runtimeJinjaString(right))
}

func runtimeAnySlice(value any) ([]any, bool) {
	if value == nil || isRuntimeUndefined(value) {
		return nil, false
	}
	if items, ok := value.([]any); ok {
		return items, true
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	out := make([]any, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out = append(out, rv.Index(i).Interface())
	}
	return out, true
}

func runtimeIsMapping(value any) bool {
	if value == nil || isRuntimeUndefined(value) {
		return false
	}
	if _, ok := value.(map[string]any); ok {
		return true
	}
	rv := reflect.ValueOf(value)
	return rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String
}

func runtimeMapLookup(value any, key string) (any, bool) {
	if value == nil || isRuntimeUndefined(value) {
		return nil, false
	}
	if mapped, ok := value.(map[string]any); ok {
		value, exists := mapped[key]
		return value, exists
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	got := rv.MapIndex(reflect.ValueOf(key))
	if !got.IsValid() {
		return nil, false
	}
	return got.Interface(), true
}

func runtimeMapKeys(value any) []string {
	if value == nil || isRuntimeUndefined(value) {
		return nil
	}
	if mapped, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(mapped))
		for key := range mapped {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return nil
	}
	keys := make([]string, 0, rv.Len())
	for _, key := range rv.MapKeys() {
		keys = append(keys, key.String())
	}
	sort.Strings(keys)
	return keys
}

func runtimeItems(value any) []any {
	keys := runtimeMapKeys(value)
	out := make([]any, 0, len(keys))
	for _, key := range keys {
		itemValue, _ := runtimeMapLookup(value, key)
		out = append(out, map[string]any{"key": key, "value": itemValue})
	}
	return out
}

func (runtimeLoopControl) Error() string {
	return "runtime Jinja loop control"
}
