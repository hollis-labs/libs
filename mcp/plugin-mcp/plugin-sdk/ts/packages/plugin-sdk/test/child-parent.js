// Test-only OS child supervisor. Protocol bytes always enter the SDK reader/writer.
import { spawn } from "node:child_process";
import { mkdtemp, writeFile, rename, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parseJSONTokens, validatePortableJSON } from "../dist/strict-json.js";
export const PARENT_LIMITS = Object.freeze({
  frame: 8388608,
  frames: 32,
  bytes: 8388608,
  diagnostics: 65536,
  event: 4096,
  events: 32,
  eventBytes: 65536,
  stderrTotal: 1048576,
  watchdog: 20000,
  step: 5000,
  grace: 1000,
  reap: 2000,
});
export class HarnessError extends Error {
  constructor(code) {
    super(code);
    this.name = "HarnessError";
    this.code = code;
  }
}
function deferred() {
  let resolve;
  const promise = new Promise((r) => {
    resolve = r;
  });
  return { promise, resolve };
}
export class BoundedMailbox {
  constructor(frames, bytes) {
    this.maxFrames = frames;
    this.maxBytes = bytes;
    this.items = [];
    this.bytes = 0;
  }
  push(value, bytes) {
    if (this.failure) return;
    if (this.waiter?.match(value)) {
      const waiter = this.waiter;
      this.waiter = undefined;
      waiter.resolve(value);
      return;
    }
    if (
      this.items.length >= this.maxFrames ||
      bytes > this.maxBytes - this.bytes
    )
      throw new HarnessError("parent_queue_limit");
    this.items.push({ value, bytes });
    this.bytes += bytes;
  }
  async take(match = () => true) {
    const index = this.items.findIndex((item) => match(item.value));
    if (index >= 0) {
      const [item] = this.items.splice(index, 1);
      this.bytes -= item.bytes;
      return item.value;
    }
    if (this.failure) throw this.failure;
    if (this.ended) throw new HarnessError("unexpected_eof");
    if (this.waiter) throw new HarnessError("parallel_expectation");
    return new Promise((resolve, reject) => {
      this.waiter = { match, resolve, reject };
    });
  }
  end() {
    this.ended = true;
    if (this.waiter) {
      this.waiter.reject(new HarnessError("unexpected_eof"));
      this.waiter = undefined;
    }
  }
  fail(error) {
    this.failure = error;
    if (this.waiter) {
      this.waiter.reject(error);
      this.waiter = undefined;
    }
  }
}
class Lines {
  constructor(limit, receive) {
    this.buffer = Buffer.alloc(limit);
    this.length = 0;
    this.receive = receive;
  }
  push(chunk) {
    for (let offset = 0; offset < chunk.length; ) {
      const lf = chunk.indexOf(10, offset),
        end = lf < 0 ? chunk.length : lf,
        bytes = end - offset;
      if (this.length + bytes + 1 > this.buffer.length)
        throw new HarnessError("parent_frame_limit");
      chunk.copy(this.buffer, this.length, offset, end);
      this.length += bytes;
      if (lf >= 0) {
        const raw = this.buffer.subarray(0, this.length);
        const line = new TextDecoder("utf-8", {
          fatal: true,
          ignoreBOM: true,
        }).decode(raw.at(-1) === 13 ? raw.subarray(0, -1) : raw);
        this.receive(line, this.length + 1);
        this.length = 0;
      }
      offset = end + 1;
    }
  }
  end() {
    if (this.length) throw new HarnessError("truncated_stdout");
  }
}
const children = new Set();
let starting = 0;
export class ChildSession {
  static async start(
    command,
    args,
    { pathControl = false, env = {}, limits = {}, signal } = {},
  ) {
    for (const [key, value] of Object.entries(limits))
      if (
        !Object.hasOwn(PARENT_LIMITS, key) ||
        !Number.isSafeInteger(value) ||
        value < 1 ||
        value > PARENT_LIMITS[key]
      )
        throw new HarnessError("invalid_parent_limits");
    if (children.size + starting >= 1)
      throw new HarnessError("parent_child_limit");
    starting++;
    let root;
    try {
      root = await mkdtemp(join(process.env.TMPDIR ?? tmpdir(), "sdk-child-"));
      const control = join(root, "control.json");
      if (pathControl) await writeFile(control, "", { mode: 0o600 });
      const child = spawn(
        command,
        pathControl
          ? args.map((arg) =>
              arg === "--fixture-control-permission"
                ? "--allow-read=" + root
                : arg,
            )
          : args,
        {
          env: {
            PATH: process.env.PATH,
            TMPDIR: root,
            ...env,
            ...(pathControl ? { SDK_FIXTURE_CONTROL_PATH: control } : {}),
          },
          stdio: ["pipe", "pipe", "pipe", pathControl ? "ignore" : "pipe"],
        },
      );
      return new ChildSession(
        child,
        root,
        pathControl ? control : undefined,
        { ...PARENT_LIMITS, ...limits },
        signal,
      );
    } catch (error) {
      if (root) await rm(root, { recursive: true, force: true });
      throw error;
    } finally {
      starting--;
    }
  }
  constructor(child, root, path, limits, signal) {
    children.add(this);
    this.detachAbort = () => signal?.removeEventListener("abort", abort);
    const abort = () => this.fail(new HarnessError("parent_aborted"));
    this.child = child;
    this.root = root;
    this.path = path;
    this.limits = limits;
    this.frames = new BoundedMailbox(limits.frames, limits.bytes);
    this.events = new BoundedMailbox(limits.events, limits.eventBytes);
    this.closed = deferred();
    this.stdoutClosed = deferred();
    this.seq = 0;
    this.stderrBytes = 0;
    this.diagnostics = Buffer.alloc(0);
    this.stdout = new Lines(limits.frame, (line, bytes) => {
      validatePortableJSON(line);
      this.frames.push({ raw: line, value: parseJSONTokens(line) }, bytes);
    });
    this.stderr = new Lines(limits.event, (line, bytes) => {
      if (line.startsWith('{"fixture_event":')) {
        const event = JSON.parse(line).fixture_event;
        this.events.push(event, bytes);
      }
    });
    child.once("error", () => this.fail(new HarnessError("spawn_failed")));
    child.stdin.on("error", () => {
      if (!this.allowInputClose) this.fail(new HarnessError("stdin_failed"));
    });
    child.stdio[3]?.on("error", () =>
      this.fail(new HarnessError("control_failed")),
    );
    child.stdout.on("data", (chunk) => {
      try {
        this.stdout.push(chunk);
      } catch (error) {
        this.fail(
          error instanceof HarnessError
            ? error
            : new HarnessError("invalid_stdout"),
        );
      }
    });
    child.stdout.once("end", () => {
      try {
        this.stdout.end();
        this.frames.end();
      } catch (error) {
        this.fail(error);
      }
      this.stdoutClosed.resolve();
    });
    child.stdout.on("error", () => {
      this.fail(new HarnessError("stdout_failed"));
      this.stdoutClosed.resolve();
    });
    child.stderr.on("data", (chunk) => {
      try {
        this.stderrBytes += chunk.length;
        if (this.stderrBytes > limits.stderrTotal)
          throw new HarnessError("stderr_limit");
        this.diagnostics = Buffer.concat([this.diagnostics, chunk]).subarray(
          -limits.diagnostics,
        );
        this.stderr.push(chunk);
      } catch (error) {
        this.fail(
          error instanceof HarnessError
            ? error
            : new HarnessError("invalid_fixture_event"),
        );
      }
    });
    child.stderr.on("error", () =>
      this.fail(new HarnessError("stderr_failed")),
    );
    child.once("close", (code, signal) => {
      clearTimeout(this.watchdog);
      children.delete(this);
      this.detachAbort();
      this.exit = { code, signal };
      this.frames.end();
      this.events.end();
      this.stdoutClosed.resolve();
      this.closed.resolve(this.exit);
    });
    signal?.addEventListener("abort", abort, { once: true });
    this.watchdog = setTimeout(
      () => this.fail(new HarnessError("scenario_watchdog")),
      limits.watchdog,
    );
    if (signal?.aborted) abort();
  }
  fail(error) {
    if (this.failure) return;
    this.failure = error;
    this.frames.fail(error);
    this.events.fail(error);
    this.child.stdout.destroy();
    this.child.stderr.destroy();
    this.stdoutClosed.resolve();
    void this.terminate().catch(() => {});
  }
  async bounded(promise, code = "step_watchdog", ms = this.limits.step) {
    let timer;
    try {
      return await Promise.race([
        promise,
        new Promise((_, reject) => {
          timer = setTimeout(() => {
            const error = new HarnessError(code);
            this.fail(error);
            reject(error);
          }, ms);
        }),
      ]);
    } finally {
      clearTimeout(timer);
    }
  }
  async write(bytes, stream = this.child.stdin) {
    if (this.failure) throw this.failure;
    if (this.writing) throw new HarnessError("parallel_parent_write");
    this.writing = true;
    try {
      for (let i = 0; i < bytes.length; i += 65536) {
        const part = bytes.subarray(i, i + 65536);
        await this.bounded(
          new Promise((resolve, reject) =>
            stream.write(part, (error) =>
              error ? reject(new HarnessError("stdin_failed")) : resolve(),
            ),
          ),
        );
      }
    } finally {
      this.writing = false;
    }
  }
  async send(raw, ending = "\n") {
    const bytes = Buffer.from(raw + ending);
    if (bytes.length > this.limits.bytes)
      throw new HarnessError("parent_input_limit");
    await this.write(bytes);
  }
  async sendStep(step) {
    let raw = step.raw ?? JSON.stringify(step.send);
    const bytes = Buffer.from(raw);
    const padded = step.pad_bytes ?? bytes.length,
      repeat = step.repeat ?? 1;
    if (
      !Number.isSafeInteger(padded) ||
      padded < bytes.length ||
      !Number.isSafeInteger(repeat) ||
      repeat < 1 ||
      padded * repeat > 33554432
    )
      throw new HarnessError("invalid_fixture_expansion");
    if (padded > 0 && padded <= 65536) {
      // Batch whole raw-plus-padding units; never one OS write per repeated byte.
      const unit = Buffer.alloc(padded, 32);
      bytes.copy(unit);
      const perChunk = Math.floor(65536 / padded);
      const chunk = Buffer.alloc(perChunk * padded).fill(unit);
      for (let left = repeat; left > 0; left -= perChunk)
        await this.write(chunk.subarray(0, Math.min(left, perChunk) * padded));
    } else if (padded > 0) {
      const space = Buffer.alloc(65536, 32);
      for (let n = 0; n < repeat; n++) {
        await this.write(bytes);
        for (let left = padded - bytes.length; left > 0; left -= 65536)
          await this.write(space.subarray(0, Math.min(left, 65536)));
      }
    }
    await this.write(Buffer.from(step.crlf ? "\r\n" : "\n"));
  }
  async frame(match = () => true) {
    return this.bounded(this.frames.take((frame) => match(frame.value)));
  }
  async event(match) {
    return this.bounded(this.events.take(match));
  }
  async release(gate) {
    const raw = JSON.stringify({ seq: ++this.seq, op: "release", gate });
    if (Buffer.byteLength(raw) + 1 > this.limits.event)
      throw new HarnessError("control_limit");
    if (this.path) {
      const next = this.path + ".next";
      await writeFile(next, raw, { mode: 0o600 });
      await rename(next, this.path);
    } else await this.write(Buffer.from(raw + "\n"), this.child.stdio[3]);
    await this.event(
      (event) => event.kind === "control_received" && event.seq === this.seq,
    );
  }
  async end() {
    if (!this.inputEnded) {
      this.inputEnded = true;
      this.child.stdin.end();
    }
  }
  signal(name) {
    if (!["SIGTERM", "SIGKILL"].includes(name))
      throw new HarnessError("invalid_signal");
    this.child.kill(name);
  }
  async wait() {
    this.child.stdio[3]?.end();
    return this.bounded(this.closed.promise, "exit_watchdog");
  }
  async terminate() {
    if (this.termination) return this.termination;
    this.termination = (async () => {
      if (!this.exit) {
        this.child.stdin.destroy();
        this.child.stdio[3]?.destroy();
        this.child.kill("SIGTERM");
        let timer;
        await Promise.race([
          this.closed.promise,
          new Promise((resolve) => {
            timer = setTimeout(resolve, this.limits.grace);
          }),
        ]);
        clearTimeout(timer);
        if (!this.exit) this.child.kill("SIGKILL");
        let reap;
        try {
          await Promise.race([
            this.closed.promise,
            new Promise((_, reject) => {
              reap = setTimeout(
                () => reject(new HarnessError("unreaped_child")),
                this.limits.reap,
              );
            }),
          ]);
        } finally {
          clearTimeout(reap);
        }
      }
      await this.stdoutClosed.promise;
    })();
    return this.termination;
  }
  async dispose() {
    clearTimeout(this.watchdog);
    await this.terminate();
    await rm(this.root, { recursive: true, force: true });
  }
}
