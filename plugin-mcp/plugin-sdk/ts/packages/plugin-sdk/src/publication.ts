import type { Writable } from 'node:stream';
import { WriteTimeoutError } from './frame-codec.js';
import { CorrelationError } from './correlation.js';

type Publication = { frame: string; resolve: () => void; reject: (error: unknown) => void };

/** One physical writer with bounded, nonwaiting admission and write receipts. */
export class FrameWriter {
  private queue: Publication[] = [];
  private active = false;
  private failure: unknown;
  private sealed = false;
  private abortActive?: (error: unknown) => void;
  private readonly drained: Promise<void>;
  private resolveDrained!: () => void;
  private readonly output: Writable;
  private readonly timeout: number;
  private readonly fence: (error: unknown) => void;

  constructor(output: Writable, timeout: number, fence: (error: unknown) => void) {
    this.output = output;
    this.timeout = timeout;
    this.fence = fence;
    this.drained = new Promise(resolve => { this.resolveDrained = resolve; });
  }

  publish(frame: string): Promise<void> {
    if (this.sealed || this.failure) return Promise.reject(this.failure ?? new Error('connection closed'));
    if (this.queue.length >= 32) return Promise.reject(new CorrelationError());
    return new Promise((resolve, reject) => {
      this.queue.push({ frame, resolve, reject });
      this.pump();
    });
  }

  abort(error: unknown): void {
    this.failure ??= error;
    this.sealed = true;
    for (const item of this.queue.splice(0)) item.reject(error);
    const abort = this.abortActive;
    this.abortActive = undefined;
    abort?.(error);
    this.wake();
  }

  async flush(): Promise<void> {
    this.sealed = true;
    this.wake();
    await this.drained;
    if (this.failure) throw this.failure;
  }

  private wake(): void {
    if (this.sealed && !this.active && !this.queue.length) this.resolveDrained();
  }

  private pump(): void {
    if (this.active) return;
    const item = this.queue.shift();
    if (!item) { this.wake(); return; }
    this.active = true;
    let settled = false;
    const finish = (error?: unknown): void => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      this.active = false;
      this.abortActive = undefined;
      if (error) {
        item.reject(error);
        this.abort(error);
        this.fence(error);
      } else {
        item.resolve();
        this.pump();
      }
      this.wake();
    };
    this.abortActive = finish;
    const timer = setTimeout(() => finish(new WriteTimeoutError()), this.timeout);
    try { this.output.write(item.frame, error => finish(error ?? undefined)); }
    catch (error) { finish(error); }
  }
}
