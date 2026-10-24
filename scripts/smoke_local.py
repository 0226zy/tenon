#!/usr/bin/env python3
"""Trusted-local end-to-end smoke test with deterministic process cleanup."""
from __future__ import annotations

import argparse
import http.client
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
LOCAL = ROOT / ".local"
BASE = "http://127.0.0.1:8080"
ORIGIN = BASE


def request(opener, method, path, body=None, expected=(200,), extra=None):
    data = None if body is None else json.dumps(body).encode()
    headers = {"Accept": "application/json"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    if method not in ("GET", "HEAD"):
        headers["Origin"] = ORIGIN
    if extra:
        headers.update(extra)
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with opener.open(req, timeout=120) as resp:
            raw = resp.read()
            status = resp.status
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        status = exc.code
    if status not in expected:
        raise AssertionError(f"{method} {path}: HTTP {status}, body={raw[:2000]!r}")
    if not raw:
        return status, None
    content_type = "application/json"
    try:
        return status, json.loads(raw)
    except json.JSONDecodeError:
        return status, raw.decode(errors="replace")


def ensure_demo_account():
    LOCAL.mkdir(mode=0o700, exist_ok=True)
    password_file = LOCAL / "demo-password"
    if password_file.exists():
        password = password_file.read_text().strip()
    else:
        password = secrets.token_urlsafe(18)
        password_file.write_text(password + "\n")
        password_file.chmod(0o600)
    env = dict(os.environ, CONTROL_PASSWORD=password)
    add = subprocess.run(
        [str(ROOT / "bin/control"), "user", "add", "-config", "deploy/control.local.yaml", "-user", "demo", "-name", "本地验收"],
        cwd=ROOT, env=env, text=True, capture_output=True,
    )
    if add.returncode:
        subprocess.run(
            [str(ROOT / "bin/control"), "user", "passwd", "-config", "deploy/control.local.yaml", "-user", "demo"],
            cwd=ROOT, env=env, check=True, text=True, capture_output=True,
        )
    (LOCAL / "demo-username").write_text("demo\n")
    return "demo", password


def wait_api(proc):
    deadline = time.time() + 30
    while time.time() < deadline:
        if proc.poll() is not None:
            raise RuntimeError(f"control exited with {proc.returncode}")
        try:
            urllib.request.urlopen(BASE + "/api/models", timeout=1)
        except urllib.error.HTTPError as exc:
            if exc.code == 401:
                return
        except OSError:
            pass
        time.sleep(0.25)
    raise TimeoutError("control API did not become ready")


def preview_smoke(url):
    parsed = urllib.parse.urlsplit(url)
    host = parsed.netloc
    port = parsed.port or 80
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=30)
    path = parsed.path + "?" + parsed.query
    conn.request("GET", path, headers={"Host": host})
    resp = conn.getresponse()
    body = resp.read()
    if resp.status != 302:
        raise AssertionError(f"preview enter: {resp.status} {body[:1000]!r}")
    cookies = [v.split(";", 1)[0] for v in resp.headers.get_all("Set-Cookie", [])]
    conn.request("GET", "/", headers={"Host": host, "Cookie": "; ".join(cookies)})
    resp = conn.getresponse()
    body = resp.read()
    if resp.status != 200:
        raise AssertionError(f"preview root: {resp.status} {body[:2000]!r}")
    conn.close()
    return len(body)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--message", default="构建一个本地产品验收看板，展示清晰的任务状态。")
    args = parser.parse_args()
    username, password = ensure_demo_account()
    log_path = LOCAL / "control-smoke.log"
    with log_path.open("ab", buffering=0) as log:
        proc = subprocess.Popen(
            [str(ROOT / "bin/control"), "serve", "-config", "deploy/control.local.yaml"],
            cwd=ROOT, stdout=log, stderr=subprocess.STDOUT,
        )
        try:
            wait_api(proc)
            jar = http.cookiejar.CookieJar()
            opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
            request(opener, "POST", "/api/login", {"username": username, "password": password}, (204,))
            _, me = request(opener, "GET", "/api/me")
            _, models = request(opener, "GET", "/api/models")
            assert me["user_id"] == "demo" and any(m["id"] == "local-demo" for m in models)
            _, conv = request(opener, "POST", "/api/conversations", {"title": "本地全链路验收", "model": "local-demo", "template": "react-go-sqlite"}, (201,))
            cid = conv["id"]
            request_id = uuid.uuid4().hex
            accepted = {"client_request_id": request_id, "content": args.message}
            _, first = request(opener, "POST", f"/api/conversations/{cid}/messages", accepted, (202,))
            _, duplicate = request(opener, "POST", f"/api/conversations/{cid}/messages", accepted, (202,))
            assert first["run_id"] == duplicate["run_id"]
            request(opener, "POST", f"/api/conversations/{cid}/messages", {"client_request_id": request_id, "content": "different"}, (409,))
            rid = first["run_id"]
            deadline = time.time() + 300
            terminal = None
            while time.time() < deadline:
                _, run = request(opener, "GET", f"/api/conversations/{cid}/runs/{rid}")
                if run["status"] in ("finished", "failed", "cancelled"):
                    terminal = run
                    break
                time.sleep(1)
            if terminal is None:
                raise TimeoutError(f"Run {rid} did not finish")
            if terminal["status"] != "finished":
                _, history = request(opener, "GET", f"/api/conversations/{cid}/events/history?after=0&limit=200")
                raise AssertionError(f"Run failed: {terminal}; events={history}")
            _, history = request(opener, "GET", f"/api/conversations/{cid}/events/history?after=0&limit=200")
            tool_results = {
                item["data"].get("name"): item["data"]
                for frame in history["items"]
                for item in frame["items"]
                if item["type"] == "tool_result"
            }
            for name in ("write_file", "run_tests", "preview"):
                assert name in tool_results, f"missing tool result: {name}; events={history}"
                assert not tool_results[name].get("is_error"), f"tool {name} failed: {tool_results[name]}"
            _, tree = request(opener, "GET", f"/api/conversations/{cid}/files/tree?path=")
            assert any(item["path"] in ("web", "web/") or item["name"] == "web" for item in tree)
            _, app = request(opener, "GET", f"/api/conversations/{cid}/files/content?path=web/src/App.tsx")
            assert "LOCAL DEMO" in app["content"]
            preview = None
            preview_deadline = time.time() + 120
            while time.time() < preview_deadline:
                _, preview = request(opener, "GET", f"/api/conversations/{cid}/preview")
                if preview["status"] in ("ready", "failed"):
                    break
                time.sleep(1)
            if preview is None or preview["status"] != "ready":
                _, preview_logs = request(opener, "GET", f"/api/conversations/{cid}/preview/logs?tail=200")
                raise AssertionError(f"preview did not become ready: {preview}; logs:\n{preview_logs}")
            assert not preview["stale"], preview
            assert preview["commit"] == terminal["commit"], (preview, terminal)
            preview_bytes = preview_smoke(preview["url"])
            _, renamed = request(opener, "PATCH", f"/api/conversations/{cid}", {"title": "Chrome 验收工作区"})
            assert renamed["title"] == "Chrome 验收工作区"
            result = {"ok": True, "conversation_id": cid, "run_id": rid, "commit": terminal.get("commit"), "preview_bytes": preview_bytes, "tools_successful": ["write_file", "run_tests", "preview"]}
            (LOCAL / "last-smoke.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
            print(json.dumps(result, ensure_ascii=False))
        except Exception:
            if log_path.exists():
                lines = log_path.read_text(errors="replace").splitlines()
                sys.stderr.write("\n--- control log tail ---\n" + "\n".join(lines[-100:]) + "\n")
            raise
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(25)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(5)


if __name__ == "__main__":
    main()
