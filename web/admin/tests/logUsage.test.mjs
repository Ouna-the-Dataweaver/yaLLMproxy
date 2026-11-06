import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const source = await readFile(new URL("../src/logUsage.ts", import.meta.url), "utf8");
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ESNext } });
const { logTokenUsage } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);

test("missing cache usage remains unreported while explicit zero is displayed", () => {
  assert.equal(logTokenUsage({}).cached, undefined);
  assert.equal(logTokenUsage({ usage_stats: { prompt_tokens_details: { cached_tokens: null } } }).cached, undefined);
  assert.equal(logTokenUsage({ usage_stats: { prompt_tokens_details: { cached_tokens: 0 } } }).cached, 0);
  assert.equal(logTokenUsage({ cached_tokens: 0 }).cached, 0);
});

test("reads Chat Completions, Responses, and native Anthropic cache usage", () => {
  assert.equal(logTokenUsage({ usage_stats: { prompt_tokens_details: { cached_tokens: 128 } } }).cached, 128);
  assert.equal(logTokenUsage({ usage_stats: { input_tokens_details: { cached_tokens: 64 } } }).cached, 64);
  assert.equal(logTokenUsage({ usage_stats: { cache_read_input_tokens: 32 } }).cached, 32);
});

test("cache and reasoning counts remain subsets instead of inflating totals", () => {
  assert.deepEqual(logTokenUsage({ usage_stats: { input_tokens: 10, output_tokens: 5, total_tokens: 15, input_tokens_details: { cached_tokens: 8 }, output_tokens_details: { reasoning_tokens: 3 } } }), { total: 15, input: 10, output: 5, cached: 8, reasoning: 3 });
});

test("invalid values fall back to valid stored usage without hiding zero", () => {
  assert.equal(logTokenUsage({ cached_tokens: -1, usage_stats: { cached_tokens: 4 } }).cached, 4);
  assert.equal(logTokenUsage({ cached_tokens: NaN, usage_stats: { cached_tokens: 4 } }).cached, 4);
  assert.equal(logTokenUsage({ total_tokens: 0, usage_stats: { total_tokens: 7 } }).total, 0);
});
