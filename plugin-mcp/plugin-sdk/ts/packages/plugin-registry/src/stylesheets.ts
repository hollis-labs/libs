/**
 * Stylesheet injection, behind a sink so the loader never touches a document
 * unless a host wants it to.
 *
 * The DOM types here are structural and deliberately narrow: the core of this
 * package compiles without `lib: DOM`, and a host that has a real document
 * passes it in. That also makes the one subtle behaviour in here testable
 * with an ordinary object — see the `getAttribute` note in `ensure`.
 */

/** The subset of an element this sink touches. */
export interface LinkElementLike {
  getAttribute(name: string): string | null
  setAttribute(name: string, value: string): void
  remove(): void
  readonly isConnected: boolean
}

/** The subset of a document this sink touches. */
export interface DocumentLike {
  head: {
    querySelector(selectors: string): LinkElementLike | null
    appendChild(node: LinkElementLike): void
  }
  createElement(tagName: 'link'): LinkElementLike
}

/**
 * Where a plugin's stylesheet goes. A host may supply its own — to route
 * styles into a shadow root, to collect them for SSR, or to refuse them.
 */
export interface StylesheetSink {
  /** Install or update the stylesheet for a plugin. */
  ensure(pluginId: string, url: string): void
  /** Remove a plugin's stylesheet, if it has one. */
  remove(pluginId: string): void
}

/** A sink that does nothing. The default where there is no document. */
export function createNullStylesheetSink(): StylesheetSink {
  return { ensure() {}, remove() {} }
}

/**
 * A sink that maintains one `<link rel="stylesheet" data-plugin="...">` per
 * plugin in a document's head.
 */
export function createLinkStylesheetSink(doc: DocumentLike): StylesheetSink {
  const links = new Map<string, LinkElementLike>()

  return {
    ensure(pluginId, url) {
      let el = links.get(pluginId)
      if (el && !el.isConnected) {
        links.delete(pluginId)
        el = undefined
      }
      if (!el) {
        el = doc.head.querySelector(`link[data-plugin="${escapeAttributeValue(pluginId)}"]`) ?? undefined
      }
      if (el) {
        // Compare the ATTRIBUTE, not the property. An anchor-like element's
        // `href` property resolves to an absolute URL, so comparing it always
        // differs from the relative path a registry serves, and the element
        // gets rewritten on every reconcile. Copied deliberately from the
        // reference implementation, which had already hit this once.
        if (el.getAttribute('href') !== url) el.setAttribute('href', url)
        links.set(pluginId, el)
        return
      }
      const link = doc.createElement('link')
      link.setAttribute('rel', 'stylesheet')
      link.setAttribute('href', url)
      link.setAttribute('data-plugin', pluginId)
      doc.head.appendChild(link)
      links.set(pluginId, link)
    },

    remove(pluginId) {
      const el = links.get(pluginId)
      links.delete(pluginId)
      if (el?.isConnected) el.remove()
    },
  }
}

/**
 * The default sink: a link sink over the ambient document where there is one,
 * and nothing where there is not (SSR, a test, a worker).
 */
export function defaultStylesheetSink(): StylesheetSink {
  // An assertion rather than a check: the core compiles without DOM lib, so
  // there is no ambient `Document` type to narrow to. The shape is the one
  // declared above and a real document satisfies it.
  const ambient = (globalThis as { document?: DocumentLike }).document
  return ambient ? createLinkStylesheetSink(ambient) : createNullStylesheetSink()
}

function escapeAttributeValue(value: string): string {
  return value.replace(/["\\]/g, '\\$&')
}
