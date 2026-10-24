// Adhar: start the Keycloak sign-in on its own when it is the ONLY way in.
//
// Plane's sign-in page (served at "/") always draws a "Login with Keycloak"
// button and waits for a click, even when Keycloak is the only method enabled.
// The HTTPRoute sends a cookie-less visitor straight to /auth/keycloak/, but a
// visitor holding an ANONYMOUS `session-id` (Plane stores the OIDC state in a
// session before sign-in completes, so any abandoned or failed attempt leaves
// one) reaches this page and stops there. This runs before the app, only on
// "/", and only when: the API says there is no signed-in user, Keycloak is
// enabled, and no other method is. It never fires after an auth error (the
// error is shown instead of looping) or twice within 30 seconds.
(function () {
  try {
    if (location.pathname !== "/") return;
    var q = new URLSearchParams(location.search);
    if (q.has("error_code") || q.has("error_message")) return;
    var KEY = "adhar-auto-sso-at", last = Number(sessionStorage.getItem(KEY) || 0);
    if (Date.now() - last < 30000) return;
    fetch("/api/users/me/", { credentials: "include" }).then(function (me) {
      if (me.status !== 401 && me.status !== 403) return;
      return fetch("/api/instances/", { credentials: "include" }).then(function (r) { return r.json(); }).then(function (body) {
        var c = (body && body.config) || {};
        var others = ["is_email_password_enabled", "is_magic_login_enabled", "is_google_enabled",
                      "is_github_enabled", "is_gitlab_enabled", "is_gitea_enabled"];
        if (!c.is_keycloak_enabled || others.some(function (k) { return c[k]; })) return;
        sessionStorage.setItem(KEY, String(Date.now()));
        var next = q.get("next_path");
        location.replace("/auth/keycloak/" + (next ? "?next_path=" + encodeURIComponent(next) : ""));
      });
    }).catch(function () {});
  } catch (e) {}
})();
