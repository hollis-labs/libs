import { serve } from '../dist/index.js';
import { fixturePlugin } from './fixtures.js';
await serve(fixturePlugin(process.argv[2]));
