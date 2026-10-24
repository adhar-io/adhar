"""Keycloak sign-in for apps whose open-source edition sells SSO (Metabase, PostHog).

Sits between the app's oauth2-proxy and the app. The proxy has already signed
the user in through Keycloak and passes who they are in X-Forwarded-Email /
-Preferred-Username / -Groups; this bridge turns that into the app's OWN
session, so the user never sees the app's login form:

  * an account is created the first time a Keycloak user arrives (Metabase:
    admin API; PostHog: an organisation invite redeemed by the bridge), with a
    password derived from the user's email and a key only this bridge holds
    (HMAC-SHA256), so nobody — the user included — ever types it;
  * a page load without the app's session cookie, or with a cookie that belongs
    to a different Keycloak user, logs in with that password and redirects back
    to the same URL with the app's own Set-Cookie headers;
  * an API call the app answers with 401 (session expired mid-use) is logged in
    and retried once, so the single-page app never routes to its login form.

Trust: the identity headers are believed only because the bridge is reachable
from its oauth2-proxy alone (NetworkPolicy beside it), and oauth2-proxy
overwrites any X-Forwarded-* a client sends. Members of ADMIN_GROUP become app
administrators. Signing out of the app signs you straight back in; sign out of
Keycloak to end the session.

Rendered into both packages by hack/sso-bridge/render.sh; edit THIS file.
"""

import hashlib
import hmac
import http.client
import json
import os
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODE = os.environ["MODE"]  # metabase | posthog
UPSTREAM = urllib.parse.urlsplit(os.environ["UPSTREAM"])
KEY = os.environ["BRIDGE_SECRET"].encode()
ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "").strip().lower()
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "")
API_KEY = os.environ.get("API_KEY", "")
ADMIN_GROUP = os.environ.get("ADMIN_GROUP", "platform-admin")
OWNER_COOKIE = "adhar_sso_owner"
SESSION_COOKIE = {"metabase": "metabase.SESSION", "posthog": "sessionid"}[MODE]
LOGIN_PAGES = {"metabase": ("/auth/login",), "posthog": ("/login",)}[MODE]
HOP = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
       "te", "trailer", "transfer-encoding", "upgrade", "content-length"}


def log(*a):
    print(time.strftime("%Y-%m-%dT%H:%M:%S"), *a, file=sys.stderr, flush=True)


def derived_password(email):
    # Upper, lower, digit and symbol, so every complexity rule either app ships accepts it.
    return "Ad1-" + hmac.new(KEY, b"pw:" + email.encode(), hashlib.sha256).hexdigest()


def owner_tag(email):
    return hmac.new(KEY, b"owner:" + email.encode(), hashlib.sha256).hexdigest()[:32]


def call(method, path, body=None, headers=None):
    """One request to the app; returns (status, headers list, parsed JSON or None)."""
    conn = http.client.HTTPConnection(UPSTREAM.hostname, UPSTREAM.port or 80, timeout=30)
    h = {"content-type": "application/json", "accept": "application/json", **(headers or {})}
    try:
        conn.request(method, path, json.dumps(body) if body is not None else None, h)
        r = conn.getresponse()
        raw = r.read()
        try:
            data = json.loads(raw) if raw else None
        except ValueError:
            data = None
        return r.status, r.getheaders(), data
    finally:
        conn.close()


def session_cookies(headers):
    return [v for k, v in headers if k.lower() == "set-cookie"]


# ── Metabase ────────────────────────────────────────────────────────────────
_mb_admin = {"session": None}


def mb_admin(method, path, body=None):
    for attempt in (0, 1):
        if not _mb_admin["session"]:
            st, _, d = call("POST", "/api/session", {"username": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
            if st != 200 or not d:
                raise RuntimeError("metabase admin login failed: %s" % st)
            _mb_admin["session"] = d["id"]
        st, h, d = call(method, path, body, {"x-metabase-session": _mb_admin["session"]})
        if st == 401 and attempt == 0:
            _mb_admin["session"] = None
            continue
        return st, d
    return st, d


def metabase_login(email, name, admin):
    if email == ADMIN_EMAIL:
        st, h, _ = call("POST", "/api/session", {"username": email, "password": ADMIN_PASSWORD})
        return session_cookies(h) if st == 200 else None
    pw = derived_password(email)
    st, h, _ = call("POST", "/api/session", {"username": email, "password": pw})
    if st != 200:
        _, d = mb_admin("GET", "/api/user?status=all&query=" + urllib.parse.quote(email))
        users = (d or {}).get("data", []) if isinstance(d, dict) else (d or [])
        user = next((u for u in users if (u.get("email") or "").lower() == email), None)
        first, _, last = (name or email.split("@")[0]).partition(" ")
        if user is None:
            st, d = mb_admin("POST", "/api/user", {"first_name": first, "last_name": last or None,
                                                   "email": email, "password": pw})
            if st != 200:
                log("metabase: cannot create", email, st, d)
                return None
            user = d
            log("metabase: created", email)
        else:
            if not user.get("is_active", True):
                mb_admin("PUT", "/api/user/%s/reactivate" % user["id"])
            mb_admin("PUT", "/api/user/%s/password" % user["id"], {"password": pw})
        st, h, _ = call("POST", "/api/session", {"username": email, "password": pw})
    if st != 200:
        return None
    # Keycloak decides who administers Metabase, on every sign-in, so removing
    # someone from the admin group takes effect at their next session.
    sid = next((v.split("=", 1)[1] for v in (cookie_pair(c) for c in session_cookies(h))
                if v.startswith("metabase.SESSION=")), None)
    _, _, me = call("GET", "/api/user/current", headers={"x-metabase-session": sid or ""})
    if me and bool(me.get("is_superuser")) != admin:
        mb_admin("PUT", "/api/user/%s" % me["id"], {"is_superuser": admin})
    return session_cookies(h)


# ── PostHog ─────────────────────────────────────────────────────────────────
def ph_api(method, path, body=None):
    st, _, d = call(method, path, body, {"authorization": "Bearer " + API_KEY})
    return st, d


def posthog_sync_level(email, admin):
    # Keycloak decides who administers the organisation, on every sign-in. An
    # owner (level 15) is never demoted: the organisation must keep one.
    _, d = ph_api("GET", "/api/organizations/@current/members/?limit=500")
    member = next((m for m in (d or {}).get("results", [])
                   if (m.get("user") or {}).get("email", "").lower() == email), None)
    want = 8 if admin else 1
    if member and member.get("level") != want and member.get("level") != 15:
        ph_api("PATCH", "/api/organizations/@current/members/%s/" % member["user"]["uuid"], {"level": want})


def posthog_login(email, name, admin):
    if email == ADMIN_EMAIL:
        st, h, _ = call("POST", "/api/login/", {"email": email, "password": ADMIN_PASSWORD})
        return session_cookies(h) if st == 200 else None
    pw = derived_password(email)
    st, h, _ = call("POST", "/api/login/", {"email": email, "password": pw})
    if st == 200:
        posthog_sync_level(email, admin)
        return session_cookies(h)
    # No account yet: an invite the bridge redeems itself. Owner-level for
    # platform admins, member otherwise (PostHog levels: 1 member, 8 admin).
    first = (name or email.split("@")[0]).split(" ")[0]
    _, d = ph_api("GET", "/api/organizations/@current/invites/?limit=200")
    invite = next((i for i in (d or {}).get("results", []) if (i.get("target_email") or "").lower() == email), None)
    if invite is None:
        st, invite = ph_api("POST", "/api/organizations/@current/invites/",
                            {"target_email": email, "first_name": first, "level": 8 if admin else 1})
        if st not in (200, 201):
            log("posthog: cannot invite", email, st, invite)
            return None
    st, h, d = call("POST", "/api/signup/%s/" % invite["id"], {"first_name": first, "password": pw})
    if st in (200, 201):
        log("posthog: created", email)
        return session_cookies(h)
    # An account this bridge did not create (its password is not ours): leave
    # the user on PostHog's own login form rather than guess.
    log("posthog: cannot sign in", email, st, d)
    return None


LOGIN = {"metabase": metabase_login, "posthog": posthog_login}[MODE]

# Concurrent requests from one page must not each open a session: the first
# logs in, the rest reuse its cookies for a few seconds.
_locks, _recent, _guard = {}, {}, threading.Lock()


def login(email, name, admin):
    with _guard:
        lock = _locks.setdefault(email, threading.Lock())
    with lock:
        hit = _recent.get(email)
        if hit and time.time() - hit[0] < 10:
            return hit[1]
        try:
            cookies = LOGIN(email, name, admin)
        except Exception as e:  # the app is down or slow: fall through to it
            log("login error", email, repr(e))
            cookies = None
        if cookies:
            _recent[email] = (time.time(), cookies)
        return cookies


def cookie_pair(set_cookie):
    return set_cookie.split(";", 1)[0].strip()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def body(self):
        if self.headers.get("transfer-encoding", "").lower() == "chunked":
            out = b""
            while True:
                size = int(self.rfile.readline().split(b";")[0], 16)
                if size == 0:
                    self.rfile.readline()
                    return out
                out += self.rfile.read(size)
                self.rfile.readline()
        n = int(self.headers.get("content-length") or 0)
        return self.rfile.read(n) if n else None

    def identity(self):
        email = (self.headers.get("x-forwarded-email") or "").strip().lower()
        name = self.headers.get("x-forwarded-preferred-username") or ""
        if "@" in name:  # realms that sign in by email make the username the email
            name = name.split("@")[0]
        groups = [g.strip().lstrip("/") for g in (self.headers.get("x-forwarded-groups") or "").split(",")]
        return email, name, ADMIN_GROUP in groups

    def cookies(self):
        jar = {}
        for part in (self.headers.get("cookie") or "").split(";"):
            k, _, v = part.strip().partition("=")
            if k:
                jar[k] = v
        return jar

    def owner_cookie(self, email):
        return "%s=%s; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=1209600" % (OWNER_COOKIE, owner_tag(email))

    def handle_any(self):
        self._extra = []  # one handler serves every request on a kept-alive connection
        if self.path == "/_bridge/healthz":
            return self.reply(200, [], b"ok")
        data = self.body()
        email, name, admin = self.identity()
        jar = self.cookies()
        path = urllib.parse.urlsplit(self.path).path
        navigation = self.command == "GET" and (
            self.headers.get("sec-fetch-mode") == "navigate" or "text/html" in (self.headers.get("accept") or ""))
        stale = SESSION_COOKIE not in jar or jar.get(OWNER_COOKIE) != (owner_tag(email) if email else None)
        if email and navigation and (stale or path in LOGIN_PAGES):
            cookies = login(email, name, admin)
            if cookies:
                target = self.path
                if path in LOGIN_PAGES:  # the app sent us to its login form: go where it wanted
                    q = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
                    target = (q.get("redirect") or q.get("next") or ["/"])[0]
                    if not target.startswith("/") or target.startswith("//"):
                        target = "/"
                return self.reply(302, [("Location", target), ("Cache-Control", "no-store")]
                                  + [("Set-Cookie", c) for c in cookies]
                                  + [("Set-Cookie", self.owner_cookie(email))], b"")
        self.forward(data, email, name, admin)

    def forward(self, data, email, name, admin, cookie_override=None, retried=False):
        conn = http.client.HTTPConnection(UPSTREAM.hostname, UPSTREAM.port or 80, timeout=600)
        headers = [(k, v) for k, v in self.headers.items() if k.lower() not in HOP]
        if cookie_override is not None:
            headers = [(k, v) for k, v in headers if k.lower() != "cookie"] + [("Cookie", cookie_override)]
        try:
            conn.putrequest(self.command, self.path, skip_host=True, skip_accept_encoding=True)
            for k, v in headers:
                conn.putheader(k, v)
            if data is not None:
                conn.putheader("Content-Length", str(len(data)))
            conn.endheaders(data)
            r = conn.getresponse()
            if (r.status == 401 and email and not retried and urllib.parse.urlsplit(self.path).path.startswith("/api/")
                    and not self.path.startswith(("/api/session", "/api/login"))):
                r.read()
                conn.close()
                cookies = login(email, name, admin)
                if cookies:
                    jar = self.cookies()
                    for c in cookies:
                        k, _, v = cookie_pair(c).partition("=")
                        jar[k] = v
                    self._extra = [("Set-Cookie", c) for c in cookies] + [("Set-Cookie", self.owner_cookie(email))]
                    return self.forward(data, email, name, admin,
                                        "; ".join("%s=%s" % kv for kv in jar.items()), True)
                return self.reply(401, [("Content-Type", "application/json")], b'{"detail":"not signed in"}')
            out = [(k, v) for k, v in r.getheaders() if k.lower() not in HOP] + self._extra
            length = r.getheader("content-length")
            self.send_response(r.status)
            for k, v in out:
                self.send_header(k, v)
            if self.command == "HEAD" or r.status in (204, 304):
                if length is not None:
                    self.send_header("Content-Length", length)
                self.end_headers()
            elif length is not None:
                self.send_header("Content-Length", length)
                self.end_headers()
                while True:
                    chunk = r.read(65536)
                    if not chunk:
                        break
                    self.wfile.write(chunk)
            else:
                self.send_header("Transfer-Encoding", "chunked")
                self.end_headers()
                while True:
                    chunk = r.read1(65536) if hasattr(r, "read1") else r.read(65536)
                    if not chunk:
                        break
                    self.wfile.write(b"%x\r\n%s\r\n" % (len(chunk), chunk))
                    self.wfile.flush()
                self.wfile.write(b"0\r\n\r\n")
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as e:
            log("upstream error", self.command, self.path, repr(e))
            try:
                self.reply(502, [("Content-Type", "text/plain")], b"upstream unavailable")
            except Exception:
                pass
        finally:
            conn.close()

    def reply(self, status, headers, body):
        self.send_response(status)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = do_HEAD = do_OPTIONS = handle_any


if __name__ == "__main__":
    ThreadingHTTPServer.daemon_threads = True
    log("sso-bridge", MODE, "->", os.environ["UPSTREAM"])
    ThreadingHTTPServer(("0.0.0.0", int(os.environ.get("PORT", "8080"))), Handler).serve_forever()
