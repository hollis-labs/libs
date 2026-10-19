import { describe, expect, it } from "vitest";
import { Engine, ErrApprovalRequired, ErrCancelled } from "../src/index.js";
import type { DispatchResult, Handle, Invocation } from "../src/index.js";
import { encode, parse } from "../src/json.js";
import { corpus, registry } from "./fixtures.js";
describe("shared Go/TypeScript conformance", () => {
  for (const tc of corpus.cases)
    it(tc.name, async () => {
      const r = registry();
      const engine = new Engine(r, tc.config);
      const scope = r.newScope({
        owner: "example",
        generation: "1",
        hooks: [tc.hook],
      });
      const seen: string[] = [];
      const handles = new Map<string, Handle>();
      const isFilter =
        (
          corpus.catalog as { definitions: { name: string; kind: string }[] }
        ).definitions.find((d) => d.name === tc.hook)!.kind === "filter";
      for (const reg of tc.registrations) {
        const handler = (i: Invocation): string | Promise<void> | void => {
          seen.push(i.payload);
          switch (reg.behavior) {
            case "work":
              return new Promise<void>((resolve) => setTimeout(resolve, 30));
            case "timeout":
              return new Promise<void>((resolve) => setTimeout(resolve, 30));
            case "nested":
              return engine
                .doAction(tc.hook, i.payload, { context: i.context })
                .then(() => {});
            case "error":
              return Promise.reject(Error("handler error"));
            case "panic":
              throw Error("handler panic");
            case "cancel":
              throw ErrCancelled;
            case "approval":
              throw ErrApprovalRequired;
            case "echo":
              return i.payload;
          }
          return reg.output;
        };
        const h = isFilter
          ? scope.addFilter(
              tc.hook,
              reg.name,
              reg.options ?? {},
              (i) => handler(i) as string | Promise<string>,
            )
          : scope.addAction(
              tc.hook,
              reg.name,
              reg.options ?? {},
              (i) => handler(i) as void | Promise<void>,
            );
        handles.set(reg.name, h);
      }
      if (tc.warnings !== undefined)
        expect(scope.registrations()[0]!.warnings).toHaveLength(tc.warnings);
      for (const name of tc.remove ?? []) scope.remove(handles.get(name)!);
      if (tc.dispose) await scope.dispose();
      try {
        for (const want of tc.expected ?? [{}]) {
          const dispatch = (): Promise<DispatchResult> =>
            isFilter
              ? engine.applyFilters(tc.hook, tc.payload)
              : tc.hook === "event.committed"
                ? Promise.resolve(
                    engine.prepareAfterCommit(tc.hook, tc.payload).commit(),
                  )
                : engine.doAction(tc.hook, tc.payload);
          if (tc.error) {
            await expect(dispatch()).rejects.toMatchObject({ code: tc.error });
            continue;
          }
          let result = await dispatch();
          if (tc.receipt) {
            expect(result.status).toBe(tc.receipt);
            result = await result.future!.await();
          }
          const expected = want as {
            status: string;
            order: string[];
            classes: string[];
            value?: string;
          };
          expect(result.status).toBe(expected.status);
          expect(result.outcomes.map((o) => o.name)).toEqual(expected.order);
          expect(result.outcomes.map((o) => o.class)).toEqual(expected.classes);
          if (expected.value === undefined)
            expect(result.value).toBeUndefined();
          else
            expect(encode(parse(result.value!))).toBe(
              encode(parse(expected.value)),
            );
        }
        if (tc.seen)
          expect(seen.map((s) => encode(parse(s)))).toEqual(
            tc.seen.map((s) => encode(parse(s))),
          );
      } finally {
        await engine.shutdown();
      }
    });
});
