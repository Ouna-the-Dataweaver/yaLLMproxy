type RecordValue = Record<string, unknown>;

function record(value: unknown): RecordValue {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as RecordValue : {};
}

function count(...values: unknown[]): number | undefined {
  return values.find((value): value is number => typeof value === "number" && Number.isFinite(value) && value >= 0);
}

export function logTokenUsage(log: RecordValue) {
  const usage = record(log.usage_stats);
  const input = record(usage.prompt_tokens_details);
  const responseInput = record(usage.input_tokens_details);
  const output = record(usage.completion_tokens_details);
  const responseOutput = record(usage.output_tokens_details);
  return {
    total: count(log.total_tokens, usage.total_tokens),
    input: count(log.prompt_tokens, usage.prompt_tokens, usage.input_tokens),
    output: count(log.completion_tokens, usage.completion_tokens, usage.output_tokens),
    cached: count(log.cached_tokens, input.cached_tokens, responseInput.cached_tokens, usage.cached_tokens, usage.cache_read_input_tokens),
    reasoning: count(log.reasoning_tokens, output.reasoning_tokens, responseOutput.reasoning_tokens, usage.reasoning_tokens)
  };
}
