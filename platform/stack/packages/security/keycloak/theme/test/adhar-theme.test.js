// Harness: run adhar-theme.js against a stub DOM and assert the resolved state.
const fs = require('fs');
const vm = require('vm');
const SRC = fs.readFileSync(process.argv[2], 'utf8');

let failures = 0;
function check(name, got, want) {
  const ok = JSON.stringify(got) === JSON.stringify(want);
  if (!ok) { failures++; console.log(`  FAIL ${name}: got ${JSON.stringify(got)} want ${JSON.stringify(want)}`); }
  else console.log(`  ok   ${name}`);
}

function makeEnv({ host = 'keycloak.platform.adhar.io', proto = 'https:', search = '', cookie = '', osDark = false } = {}) {
  const classes = new Set();
  const attrs = {};
  let cookieJar = cookie;
  const root = {
    classList: {
      add: c => classes.add(c), remove: c => classes.delete(c),
      contains: c => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    setAttribute: (k, v) => { attrs[k] = v; },
    getAttribute: k => attrs[k],
  };
  const doc = {
    documentElement: root,
    readyState: 'loading',
    addEventListener: () => {},
    getElementById: () => null,
    createElement: () => ({ setAttribute(){}, addEventListener(){}, style:{}, classList:{add(){}} }),
    get cookie() { return cookieJar; },
    set cookie(v) {
      // Record the raw Set-Cookie-ish string for assertions, and make it readable.
      lastSetCookie = v;
      const [pair] = v.split(';');
      cookieJar = cookieJar ? cookieJar + '; ' + pair : pair;
    },
  };
  let lastSetCookie = null;
  const sandbox = {
    document: doc,
    location: { hostname: host, protocol: proto, search },
    window: {
      matchMedia: q => ({ matches: osDark && /dark/.test(q), addEventListener(){}, addListener(){} }),
      MutationObserver: null,
    },
    URLSearchParams,
    get lastSetCookie() { return lastSetCookie; },
  };
  sandbox.window.document = doc;
  sandbox.MutationObserver = null;
  vm.createContext(sandbox);
  vm.runInContext(SRC, sandbox);
  return { attrs, classes, sandbox, get lastSetCookie(){ return lastSetCookie; } };
}

const DARK = 'pf-v5-theme-dark';
console.log('Explicit dark via cookie, light OS:');
let e = makeEnv({ cookie: 'adhar-theme=dark', osDark: false });
check('data-adhar-theme', e.attrs['data-adhar-theme'], 'dark');
check('dark class applied', e.classes.has(DARK), true);

console.log('Explicit light via cookie, DARK OS (the console-light-on-dark-OS case):');
e = makeEnv({ cookie: 'adhar-theme=light', osDark: true });
check('data-adhar-theme', e.attrs['data-adhar-theme'], 'light');
check('PatternFly dark class REMOVED', e.classes.has(DARK), false);

console.log('System mode, dark OS:');
e = makeEnv({ cookie: '', osDark: true });
check('data-adhar-theme', e.attrs['data-adhar-theme'], 'system');
check('dark class applied', e.classes.has(DARK), true);

console.log('System mode, light OS:');
e = makeEnv({ cookie: '', osDark: false });
check('data-adhar-theme', e.attrs['data-adhar-theme'], 'system');
check('dark class absent', e.classes.has(DARK), false);

console.log('?theme=dark overrides a light cookie and persists:');
e = makeEnv({ search: '?theme=dark', cookie: 'adhar-theme=light', osDark: false });
check('data-adhar-theme', e.attrs['data-adhar-theme'], 'dark');
check('cookie domain shared one label up', /domain=\.platform\.adhar\.io/.test(e.lastSetCookie), true);
check('cookie is secure on https', /secure/.test(e.lastSetCookie), true);
check('cookie path root', /path=\//.test(e.lastSetCookie), true);

console.log('Garbage cookie value falls back to system:');
e = makeEnv({ cookie: 'adhar-theme=wat', osDark: true });
check('data-adhar-theme', e.attrs['data-adhar-theme'], 'system');

console.log('Bare host (no siblings) must not set a domain attribute:');
e = makeEnv({ host: 'localhost', search: '?theme=dark' });
check('no domain=', /domain=/.test(e.lastSetCookie), false);

console.log('Two-label domain stays host-only:');
e = makeEnv({ host: 'example.com', search: '?theme=dark' });
check('no domain=', /domain=/.test(e.lastSetCookie), false);

console.log('IP literal stays host-only:');
e = makeEnv({ host: '10.0.0.7', search: '?theme=light', proto: 'http:' });
check('no domain=', /domain=/.test(e.lastSetCookie), false);
check('not secure on http', /secure/.test(e.lastSetCookie), false);

console.log(failures ? `\n${failures} FAILURE(S)` : '\nall assertions passed');
process.exit(failures ? 1 : 0);
