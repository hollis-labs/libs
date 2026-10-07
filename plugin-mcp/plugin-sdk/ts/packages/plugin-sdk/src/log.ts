import { stderr } from 'node:process';

export interface Logger {
  debug(message: string, fields?: Record<string, unknown>): void;
  info(message: string, fields?: Record<string, unknown>): void;
  warn(message: string, fields?: Record<string, unknown>): void;
  error(message: string, fields?: Record<string, unknown>): void;
  with(fields: Record<string, unknown>): Logger;
}
export class SecretTracker {
  private readonly values = new Set<string>();
  add(value: string): void { if (value) this.values.add(value); }
  redact(value: string): string {
    // Longest first keeps overlapping secrets from exposing a suffix.
    for (const secret of [...this.values].sort((a, b) => b.length - a.length)) value = value.split(secret).join('[REDACTED]');
    return value;
  }
}
export interface LoggerOptions {
  secrets?: SecretTracker;
  write?: (line: string) => void;
}
export function createLogger(options: LoggerOptions = {}): Logger {
  const tracker = options.secrets ?? new SecretTracker();
  const write = options.write ?? ((line: string) => { stderr.write(line); });
  function logger(base: Record<string, unknown>): Logger {
    function log(level: string, message: string, fields: Record<string, unknown> = {}): void {
      // Redact all JSON string values, including nested data and exception text.
      // Cycles/BigInt/getters must not send an unredacted fallback to stderr.
      let payload: string;
      try {
        payload = JSON.stringify({ ...base, ...fields, ts: new Date().toISOString(), level, msg: message }, (_key, value: unknown) => typeof value === 'string' ? tracker.redact(value) : value);
      } catch {
        payload = JSON.stringify({ level, msg: tracker.redact(message), marshal_error: 'unserializable log fields' });
      }
      write(payload + '\n');
    }
    return {
      debug: (message, fields) => log('debug', message, fields),
      info: (message, fields) => log('info', message, fields),
      warn: (message, fields) => log('warn', message, fields),
      error: (message, fields) => log('error', message, fields),
      with: fields => logger({ ...base, ...fields }),
    };
  }
  return logger({});
}
