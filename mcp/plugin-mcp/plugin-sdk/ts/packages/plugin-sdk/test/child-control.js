// Private fixture control. It never shares the protocol pipes or package exports.
import { createReadStream } from "node:fs";
import { open } from "node:fs/promises";
export const CONTROL_BYTES = 4096;
export function fixtureEvent(event) {
  process.stderr.write(JSON.stringify({ fixture_event: event }) + "\n");
}
function control(raw) {
  const value = JSON.parse(raw);
  if (
    !value ||
    Array.isArray(value) ||
    Object.keys(value).sort().join(",") !== "gate,op,seq" ||
    value.op !== "release" ||
    typeof value.gate !== "string" ||
    value.gate.length > 128 ||
    !Number.isSafeInteger(value.seq) ||
    value.seq < 1
  )
    throw new Error("invalid fixture control");
  return value;
}
export function fixtureControl(receive) {
  let stopped = false,
    last = 0,
    stream,
    timer;
  const accept = (raw) => {
    const value = control(raw);
    if (value.seq <= last) return;
    if (value.seq !== last + 1) throw new Error("fixture control sequence");
    last = value.seq;
    receive(value);
  };
  const fail = () => {
    fixtureEvent({ kind: "control_failure" });
    process.exitCode = 1;
    stop();
  };
  const stop = () => {
    stopped = true;
    clearTimeout(timer);
    stream?.destroy();
  };
  const path = process.env.SDK_FIXTURE_CONTROL_PATH;
  if (path) {
    const poll = async () => {
      try {
        const file = await open(path, "r");
        let bytes;
        try {
          const buffer = Buffer.alloc(CONTROL_BYTES + 1);
          const read = await file.read(buffer, 0, buffer.length, 0);
          if (read.bytesRead > CONTROL_BYTES)
            throw new Error("fixture control limit");
          bytes = buffer.subarray(0, read.bytesRead);
        } finally {
          await file.close();
        }
        if (bytes.length) accept(bytes.toString("utf8"));
      } catch {
        fail();
        return;
      }
      if (!stopped) timer = setTimeout(poll, 10);
    };
    void poll();
  } else {
    stream = createReadStream(null, {
      fd: 3,
      autoClose: true,
      highWaterMark: 1024,
    });
    let pending = Buffer.alloc(0);
    stream.on("data", (chunk) => {
      try {
        for (let start = 0; start < chunk.length; ) {
          let end = chunk.indexOf(10, start);
          if (end < 0) end = chunk.length;
          if (pending.length + end - start > CONTROL_BYTES)
            throw new Error("fixture control limit");
          pending = Buffer.concat([pending, chunk.subarray(start, end)]);
          if (end < chunk.length) {
            accept(pending.toString("utf8"));
            pending = Buffer.alloc(0);
          }
          start = end + 1;
        }
      } catch {
        fail();
      }
    });
    stream.on("error", fail);
  }
  return stop;
}
