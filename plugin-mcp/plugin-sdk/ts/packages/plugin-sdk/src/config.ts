import { SecretTracker } from './log.js';
import type { InitParams } from './wire.js';

export class ConfigReader {
  private readonly values: Record<string, string>;
  private readonly secrets: SecretTracker;
  constructor(values: Record<string, string> | null = {}, secrets = new SecretTracker()) {
    this.values = { ...values };
    this.secrets = secrets;
  }
  string(key: string): string { return Object.hasOwn(this.values, key) ? this.values[key] : ''; }
  bool(key: string): boolean { return ['1', 't', 'T', 'TRUE', 'true', 'True'].includes(this.string(key)); }
  int(key: string): number {
    const value = this.string(key);
    if (!/^[+-]?\d+$/.test(value)) return 0;
    const parsed = Number(value);
    return Number.isSafeInteger(parsed) ? parsed : 0;
  }
  secret(key: string): string { const value = this.string(key); this.secrets.add(value); return value; }
  required(key: string): string {
    const value = this.string(key);
    if (!value) throw new Error(`required config key ${JSON.stringify(key)} is missing or empty`);
    return value;
  }
  has(key: string): boolean { return Object.hasOwn(this.values, key); }
}
export function hasCapability(params: InitParams, name: string): boolean { return params.granted?.includes(name) ?? false; }
export function resolvedDataDir(params: InitParams): string {
  if (!params.data_dir) throw new Error('subprocess: InitParams.DataDir not set by host');
  return params.data_dir;
}
