/** A structural/wire failure, distinct from a named host admission refusal. */
export class RegistryError extends Error {
  readonly code: string;
  constructor(code: string) {
    super(code);
    this.name = "RegistryError";
    this.code = code;
  }
}
