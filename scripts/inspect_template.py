"""Inspect a Jinja chat template and suggest think tag formatting options."""

from __future__ import annotations

import argparse
import re
from collections import Counter
from pathlib import Path
from typing import Any, Iterable, Tuple


STRING_RE = re.compile(r"(?P<quote>['\"])(?P<body>(?:\\.|(?!\1).)*)\1", re.DOTALL)
EXPR_RE = re.compile(r"\{\{(.*?)\}\}", re.DOTALL)


def _unescape(value: str) -> str:
    try:
        return bytes(value, "utf-8").decode("unicode_escape")
    except Exception:
        return value


def _escape_yaml(value: str) -> str:
    escaped = (
        value.replace("\\", "\\\\")
        .replace("\n", "\\n")
        .replace("\r", "\\r")
        .replace("\t", "\\t")
    )
    return f"\"{escaped}\""


def _find_tag_literals(
    literals: Iterable[str],
    tag: str,
    *,
    filter_prefix_contains_lt: bool,
    filter_suffix_contains_lt: bool,
) -> list[Tuple[str, str]]:
    found: list[Tuple[str, str]] = []
    for literal in literals:
        start = 0
        while True:
            idx = literal.find(tag, start)
            if idx == -1:
                break
            prefix = literal[:idx]
            suffix = literal[idx + len(tag):]
            if filter_prefix_contains_lt and "<" in prefix:
                start = idx + len(tag)
                continue
            if filter_suffix_contains_lt and "<" in suffix:
                start = idx + len(tag)
                continue
            found.append((_unescape(prefix), _unescape(suffix)))
            start = idx + len(tag)
    return found


def _pick_candidate(candidates: list[Tuple[str, str]]) -> Tuple[str, str]:
    if not candidates:
        return "", ""
    counter = Counter(candidates)
    return counter.most_common(1)[0][0]


def _detect_include_newline(template_text: str) -> bool:
    content_pattern = re.compile(
        r"['\"][^'\"]*\\n[^'\"]*['\"]\s*\+\s*content(\.strip\(\))?",
        re.DOTALL,
    )
    return content_pattern.search(template_text) is not None


def analyze_template(template_text: str, think_tag: str = "think") -> dict[str, Any]:
    literals = [match.group("body") for match in STRING_RE.finditer(template_text)]
    expressions = [match.group(1) for match in EXPR_RE.finditer(template_text)]

    open_tag = f"<{think_tag}>"
    close_tag = f"</{think_tag}>"

    open_candidates = _find_tag_literals(
        literals,
        open_tag,
        filter_prefix_contains_lt=False,
        filter_suffix_contains_lt=True,
    )
    close_candidates = _find_tag_literals(
        literals,
        close_tag,
        filter_prefix_contains_lt=True,
        filter_suffix_contains_lt=False,
    )

    render_open_candidates: list[Tuple[str, str]] = []
    render_close_candidates: list[Tuple[str, str]] = []
    for expr in expressions:
        if "reasoning_content" not in expr:
            continue
        expr_literals = [match.group("body") for match in STRING_RE.finditer(expr)]
        render_open_candidates.extend(
            _find_tag_literals(
                expr_literals,
                open_tag,
                filter_prefix_contains_lt=False,
                filter_suffix_contains_lt=True,
            )
        )
        render_close_candidates.extend(
            _find_tag_literals(
                expr_literals,
                close_tag,
                filter_prefix_contains_lt=True,
                filter_suffix_contains_lt=False,
            )
        )

    if render_open_candidates:
        open_prefix, open_suffix = _pick_candidate(render_open_candidates)
    else:
        open_prefix, open_suffix = _pick_candidate(open_candidates)
    if render_close_candidates:
        close_prefix, close_suffix = _pick_candidate(render_close_candidates)
    else:
        close_prefix, close_suffix = _pick_candidate(close_candidates)
    include_newline = _detect_include_newline(template_text)

    config = {
        "think_tag": think_tag,
        "think_open": {
            "prefix": open_prefix,
            "suffix": open_suffix,
        },
        "think_close": {
            "prefix": close_prefix,
            "suffix": close_suffix,
        },
        "include_newline": include_newline,
    }
    return {
        "think_tag": think_tag,
        "open_candidates": open_candidates,
        "close_candidates": close_candidates,
        "render_open_candidates": render_open_candidates,
        "render_close_candidates": render_close_candidates,
        "include_newline": include_newline,
        "config": config,
    }


def extract_think_config(template_text: str, think_tag: str = "think") -> dict[str, Any]:
    return analyze_template(template_text, think_tag=think_tag)["config"]


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Inspect a Jinja template for think tag formatting hints."
    )
    parser.add_argument(
        "template",
        nargs="?",
        default="template_example.jinja",
        help="Path to the Jinja template.",
    )
    parser.add_argument(
        "--think-tag",
        default="think",
        help="Thinking tag name to inspect (default: think).",
    )
    args = parser.parse_args()

    template_path = Path(args.template)
    if not template_path.exists():
        print(f"Template not found: {template_path}")
        return 1

    template_text = template_path.read_text(encoding="utf-8")
    analysis = analyze_template(template_text, think_tag=args.think_tag)
    open_candidates = analysis["open_candidates"]
    close_candidates = analysis["close_candidates"]
    render_open_candidates = analysis["render_open_candidates"]
    render_close_candidates = analysis["render_close_candidates"]
    include_newline = analysis["include_newline"]
    config = analysis["config"]
    open_prefix = config["think_open"]["prefix"]
    open_suffix = config["think_open"]["suffix"]
    close_prefix = config["think_close"]["prefix"]
    close_suffix = config["think_close"]["suffix"]

    print(f"Template: {template_path}")
    print(f"think_tag: {args.think_tag}")

    if open_candidates:
        print("think_open candidates:")
        for prefix, suffix in sorted(set(open_candidates)):
            print(f"  - prefix={_escape_yaml(prefix)} suffix={_escape_yaml(suffix)}")
    else:
        print("think_open candidates: none detected")
    if render_open_candidates:
        print("think_open render candidates:")
        for prefix, suffix in sorted(set(render_open_candidates)):
            print(f"  - prefix={_escape_yaml(prefix)} suffix={_escape_yaml(suffix)}")

    if close_candidates:
        print("think_close candidates:")
        for prefix, suffix in sorted(set(close_candidates)):
            print(f"  - prefix={_escape_yaml(prefix)} suffix={_escape_yaml(suffix)}")
    else:
        print("think_close candidates: none detected")
    if render_close_candidates:
        print("think_close render candidates:")
        for prefix, suffix in sorted(set(render_close_candidates)):
            print(f"  - prefix={_escape_yaml(prefix)} suffix={_escape_yaml(suffix)}")

    print(f"include_newline: {'true' if include_newline else 'false'}")
    print("\nSuggested config:")
    print("swap_reasoning_content:")
    print(f"  think_tag: {args.think_tag}")
    print("  think_open:")
    print(f"    prefix: {_escape_yaml(open_prefix)}")
    print(f"    suffix: {_escape_yaml(open_suffix)}")
    print("  think_close:")
    print(f"    prefix: {_escape_yaml(close_prefix)}")
    print(f"    suffix: {_escape_yaml(close_suffix)}")
    print(f"  include_newline: {'true' if include_newline else 'false'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
