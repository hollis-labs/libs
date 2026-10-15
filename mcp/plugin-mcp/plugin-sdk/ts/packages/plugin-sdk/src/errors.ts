export class PluginError extends Error {
  readonly code: number;
  constructor(code: number, message: string) {
    super(message);
    this.name = 'PluginError';
    this.code = code;
  }
}
export class CancelledError extends Error {
  constructor() { super('plugin: action cancelled by hook'); this.name = 'CancelledError'; }
}
export const ErrCancelled = new CancelledError();
export const errNotFound = (message: string): PluginError => new PluginError(404, message);
export const errConflict = (message: string): PluginError => new PluginError(409, message);
export const errValidation = (message: string): PluginError => new PluginError(422, message);
export function errorMessage(error: unknown): string {
  try { return error instanceof Error ? error.message : `panic: ${String(error)}`; }
  catch { return 'unprintable handler error'; }
}
export function pluginError(error: unknown): { code: number; message: string } {
  const seen = new Set<unknown>();
  let typed: PluginError | undefined;
  for (let current = error; current instanceof Error && !seen.has(current);) {
    seen.add(current);
    if (current instanceof CancelledError) return { code: -32003, message: errorMessage(error) };
    if (current instanceof PluginError && !typed) typed = current;
    try { current = current.cause; } catch { break; }
  }
  if (typed) return { code: ({404: -32000, 409: -32001, 422: -32002} as Record<number, number>)[typed.code] ?? -32603, message: typed.message };
  return { code: -32603, message: errorMessage(error) };
}
