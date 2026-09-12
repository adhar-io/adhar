/* =============================================================================
   Adhar login theme — theme resolution (light / dark / follow-the-OS)

   WHY THIS FILE EXISTS
   --------------------
   Keycloak 26 ships its own dark-mode support: keycloak.v2's theme.properties
   sets `darkMode=true` + `kcDarkModeClass=pf-v5-theme-dark`, and template.ftl
   inlines a script that adds/removes that class from <html> according to
   `prefers-color-scheme`. Our stylesheet used to key off the same media query.

   Both therefore follow the OPERATING SYSTEM. Neither follows the Adhar
   Console, whose light/dark mode is an explicit user choice. Pick dark in the
   console on a light-mode laptop and the login page stayed white — the bug this
   file fixes.

   HOW THE PREFERENCE IS SHARED
   ----------------------------
   The console runs on console.<domain> and Keycloak on keycloak.<domain>.
   Different origins, so localStorage is NOT shared. A cookie scoped to the
   PARENT domain is, so the preference travels in an `adhar-theme` cookie
   (`light` | `dark` | `system`) set on `.<domain>` — readable by both.
   `?theme=` on the URL is also honoured (and persisted), which lets the console
   hand the choice over explicitly on redirect to login.

   WHY A CLASSIC SCRIPT, NOT A MODULE
   ----------------------------------
   theme.properties `scripts=` renders `<script src=… type="text/javascript">`
   in <head>, which is parser-blocking and runs BEFORE first paint — so an
   explicit choice never flashes the wrong colours. Keycloak's own dark-mode
   script is `type="module"` and therefore deferred: it runs AFTER this one and
   would clobber the choice, and it re-applies on every OS change. The observer
   at the bottom pins our decision back without overriding any template.
   ============================================================================= */
(function () {
  "use strict";

  var DARK_CLASS = "pf-v5-theme-dark";
  var COOKIE = "adhar-theme";
  var MODES = { light: 1, dark: 1, system: 1 };
  var root = document.documentElement;

  function readCookie(name) {
    var parts = ("; " + document.cookie).split("; " + name + "=");
    if (parts.length !== 2) return null;
    try {
      return decodeURIComponent(parts.pop().split(";").shift());
    } catch (e) {
      return null;
    }
  }

  /* The cookie must be visible to sibling subdomains, so it is set one label up
     (keycloak.platform.adhar.io -> .platform.adhar.io). Returning null means
     "host-only", which is correct for a bare host or an IP — there are no
     siblings to share with, and a domain attribute would be rejected anyway. */
  function cookieDomain() {
    var host = location.hostname;
    if (/^[0-9.]+$/.test(host) || host.indexOf(":") !== -1) return null;
    var labels = host.split(".");
    if (labels.length < 3) return null;
    return "." + labels.slice(1).join(".");
  }

  function writeCookie(name, value) {
    var domain = cookieDomain();
    document.cookie =
      name + "=" + encodeURIComponent(value) +
      ";path=/;max-age=31536000;samesite=lax" +
      (location.protocol === "https:" ? ";secure" : "") +
      (domain ? ";domain=" + domain : "");
  }

  function resolveMode() {
    var query = null;
    try {
      query = new URLSearchParams(location.search).get("theme");
    } catch (e) {
      /* Very old browser: fall through to the cookie. */
    }
    if (query && MODES[query]) {
      writeCookie(COOKIE, query);
      return query;
    }
    var stored = readCookie(COOKIE);
    return stored && MODES[stored] ? stored : "system";
  }

  function systemPrefersDark() {
    return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
  }

  var mode = resolveMode();
  var observer = null;

  /* `data-adhar-theme` is what the stylesheet keys off:
       - "dark"   forces the dark tokens
       - "light"  suppresses the prefers-color-scheme dark block
       - "system" leaves the media query in charge
     The PatternFly class is kept in step so the inherited component styles and
     our tokens never disagree — a light choice on a dark OS would otherwise
     leave PatternFly dark underneath our light surfaces. */
  function apply() {
    root.setAttribute("data-adhar-theme", mode);

    if (observer) {
      observer.disconnect();
      observer = null;
    }

    if (mode === "system") {
      /* Hand the class back to Keycloak's own listener. Set it once here too,
         because that listener may not have run yet. */
      root.classList.toggle(DARK_CLASS, systemPrefersDark());
      return;
    }

    var wantDark = mode === "dark";
    root.classList.toggle(DARK_CLASS, wantDark);

    /* Keycloak's deferred module re-applies the OS preference, so re-assert. */
    if (window.MutationObserver) {
      observer = new MutationObserver(function () {
        if (root.classList.contains(DARK_CLASS) !== wantDark) {
          root.classList.toggle(DARK_CLASS, wantDark);
        }
      });
      observer.observe(root, { attributes: true, attributeFilter: ["class"] });
    }
  }

  apply();

  /* ---------------------------------------------------------------------------
     Toggle control
     ---------------------------------------------------------------------------
     Without a control the shared cookie can only ever be set by the console, so
     the fix would be untestable from the login page itself. The button writes
     the SAME cookie the console reads, so the two surfaces round-trip.
     Rendered from script rather than an FTL override, keeping the theme's
     "no templates overridden" property intact.
     ------------------------------------------------------------------------- */
  var SUN =
    '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">' +
    '<circle cx="12" cy="12" r="4.2"/>' +
    '<g stroke-linecap="round"><path d="M12 2.6v2.4M12 19v2.4M2.6 12h2.4M19 12h2.4' +
    'M5.3 5.3l1.7 1.7M17 17l1.7 1.7M18.7 5.3L17 7M7 17l-1.7 1.7"/></g></svg>';
  var MOON =
    '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">' +
    '<path d="M20.1 14.6A8.4 8.4 0 1 1 9.4 3.9a6.9 6.9 0 0 0 10.7 10.7z"/></svg>';

  function effectiveDark() {
    if (mode === "dark") return true;
    if (mode === "light") return false;
    return systemPrefersDark();
  }

  function label() {
    return effectiveDark() ? "Switch to light theme" : "Switch to dark theme";
  }

  function mountToggle() {
    var header = document.getElementById("kc-header");
    if (!header || document.getElementById("adhar-theme-toggle")) return;

    var button = document.createElement("button");
    button.id = "adhar-theme-toggle";
    button.type = "button";
    button.className = "adhar-theme-toggle";
    button.setAttribute("aria-label", label());
    button.setAttribute("title", label());
    button.innerHTML = effectiveDark() ? SUN : MOON;

    button.addEventListener("click", function () {
      mode = effectiveDark() ? "light" : "dark";
      writeCookie(COOKIE, mode);
      apply();
      button.innerHTML = effectiveDark() ? SUN : MOON;
      button.setAttribute("aria-label", label());
      button.setAttribute("title", label());
    });

    header.appendChild(button);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mountToggle);
  } else {
    mountToggle();
  }

  /* In system mode the icon must track the OS as it changes. */
  if (window.matchMedia) {
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    var onChange = function () {
      if (mode !== "system") return;
      var button = document.getElementById("adhar-theme-toggle");
      if (!button) return;
      button.innerHTML = effectiveDark() ? SUN : MOON;
      button.setAttribute("aria-label", label());
      button.setAttribute("title", label());
    };
    if (mq.addEventListener) mq.addEventListener("change", onChange);
    else if (mq.addListener) mq.addListener(onChange);
  }
})();
