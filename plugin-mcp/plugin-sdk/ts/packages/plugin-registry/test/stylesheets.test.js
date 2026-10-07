/**
 * The stylesheet sink.
 *
 * The narrow structural `DocumentLike` is what makes these testable without a
 * DOM — including the one behaviour the reference implementation had to learn
 * the hard way, which is that comparing the href PROPERTY never matches a
 * relative URL.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  createLinkStylesheetSink,
  createNullStylesheetSink,
} from "../dist/index.js";

import { registry as loader, response } from "./helpers.js";

/** A document just wide enough for the sink, recording what it was asked to do. */
function fakeDocument() {
  const created = [];
  const appended = [];
  const head = {
    querySelector(selector) {
      return (
        appended.find((el) =>
          selector.includes(`"${el.attributes["data-plugin"]}"`),
        ) ?? null
      );
    },
    appendChild(node) {
      node.connected = true;
      appended.push(node);
    },
  };
  return {
    head,
    appended,
    created,
    createElement() {
      const element = {
        attributes: {},
        connected: false,
        writes: 0,
        getAttribute(name) {
          return element.attributes[name] ?? null;
        },
        setAttribute(name, value) {
          element.attributes[name] = value;
          element.writes += 1;
        },
        remove() {
          element.connected = false;
          const at = appended.indexOf(element);
          if (at >= 0) appended.splice(at, 1);
        },
        get isConnected() {
          return element.connected;
        },
      };
      created.push(element);
      return element;
    },
  };
}

test("ensure installs one link per plugin", () => {
  const doc = fakeDocument();
  const sink = createLinkStylesheetSink(doc);

  sink.ensure("acme", "/api/plugins/acme/ui/index.css");

  assert.equal(doc.appended.length, 1);
  assert.deepEqual(doc.appended[0].attributes, {
    rel: "stylesheet",
    href: "/api/plugins/acme/ui/index.css",
    "data-plugin": "acme",
  });
});

test("ensure at the same url does not rewrite the element", () => {
  // The href PROPERTY of a real link resolves to an absolute URL, so comparing
  // it against the relative path a registry serves always differs and the
  // element is rewritten on every reconcile. The sink compares the attribute.
  const doc = fakeDocument();
  const sink = createLinkStylesheetSink(doc);

  sink.ensure("acme", "/ui/index.css");
  const writesAfterCreate = doc.appended[0].writes;
  sink.ensure("acme", "/ui/index.css");

  assert.equal(doc.appended.length, 1);
  assert.equal(
    doc.appended[0].writes,
    writesAfterCreate,
    "the element was needlessly rewritten",
  );
});

test("ensure at a new url updates the existing element", () => {
  const doc = fakeDocument();
  const sink = createLinkStylesheetSink(doc);

  sink.ensure("acme", "/ui/one.css");
  sink.ensure("acme", "/ui/two.css");

  assert.equal(doc.appended.length, 1);
  assert.equal(doc.appended[0].getAttribute("href"), "/ui/two.css");
});

test("ensure re-creates a link somebody else removed", () => {
  const doc = fakeDocument();
  const sink = createLinkStylesheetSink(doc);

  sink.ensure("acme", "/ui/index.css");
  doc.appended[0].remove();
  sink.ensure("acme", "/ui/index.css");

  assert.equal(doc.appended.length, 1);
  assert.equal(doc.created.length, 2);
});

test("remove detaches the link, and is safe twice", () => {
  const doc = fakeDocument();
  const sink = createLinkStylesheetSink(doc);

  sink.ensure("acme", "/ui/index.css");
  sink.remove("acme");
  sink.remove("acme");

  assert.equal(doc.appended.length, 0);
});

test("a plugin id with quotes cannot break the attribute selector", () => {
  const doc = fakeDocument();
  const sink = createLinkStylesheetSink(doc);
  assert.doesNotThrow(() => sink.ensure('we"ird', "/ui/index.css"));
  assert.equal(doc.appended[0].getAttribute("data-plugin"), 'we"ird');
});

test("the null sink is inert", () => {
  const sink = createNullStylesheetSink();
  assert.doesNotThrow(() => {
    sink.ensure("acme", "/ui/index.css");
    sink.remove("acme");
  });
});

test("the loader installs and withdraws stylesheets as owners leave", async () => {
  const doc = fakeDocument();
  const registry = loader({ stylesheets: createLinkStylesheetSink(doc) });
  const c = response();
  c.plugins.p.stylesheet_url = "/p.css";
  await registry.sync(c);
  assert.equal(doc.appended.length, 1);
  const empty = response(2);
  empty.plugins = {};
  empty.contributions = {};
  await registry.sync(empty);
  assert.equal(doc.appended.length, 0);
});

test("stylesheets false touches no document", async () => {
  const registry = loader({ stylesheets: false });
  const c = response();
  c.plugins.p.stylesheet_url = "/p.css";
  assert.equal((await registry.sync(c)).accepted, true);
  assert.ok(registry.get("panel", "p/main"));
});
