// Internal conformance seam. Not in the package exports map or public barrel.
// The installed API cannot enable hooks; only test runners import this module.
import type { ServerPlugin } from './types.js';
const enabled = new WeakSet<object>();
export function enableHooksFixture(plugin: ServerPlugin): void { enabled.add(plugin); }
export function hooksFixtureEnabled(plugin: ServerPlugin): boolean { return enabled.has(plugin); }
