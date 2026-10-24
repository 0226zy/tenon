#!/usr/bin/env python3
import http.client
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.parse

ROOT = Path(__file__).resolve().parent.parent
DEPLOY = ROOT / "deploy"
TMP = Path(tempfile.mkdtemp(prefix=".e2e-", dir=DEPLOY))
ENV = os.environ.copy()
ENV.setdefault("GOTOOLCHAIN", "go1.25.0")
ENV.setdefault("GOPROXY", "https://goproxy.cn,direct")
CONTROL = None
CONTROL_LOG = None
PORTS = {}


def run(args, cwd=ROOT, timeout=120):
    result = subprocess.run(args, cwd=cwd, env=ENV, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"command failed ({result.returncode}): {' '.join(map(str, args))}\n{result.stdout}\n{result.stderr}")
    return result.stdout


def free_ports(count):
    sockets = []
    ports = []
    while len(ports) < count:
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
        if port not in ports and port != 3080:
            sockets.append(s)
            ports.append(port)
        else:
            s.close()
    for s in sockets:
        s.close()
    return ports


def request(port, method, path, body=None, cookie="", host="", timeout=30):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
    headers = {}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
        headers["Origin"] = f"http://127.0.0.1:{PORTS['api']}"
    if cookie:
        headers["Cookie"] = cookie
    if host:
        headers["Host"] = host
    conn.request(method, path, body=data, headers=headers)
    response = conn.getresponse()
    raw = response.read()
    result = (response.status, response.headers, raw)
    conn.close()
    return result


def api(method, path, body=None, cookie="", expected=200, timeout=30):
    status, headers, raw = request(PORTS["api"], method, path, body, cookie, timeout=timeout)
    if status != expected:
        raise RuntimeError(f"API {method} {path}: status={status}, want={expected}, body={raw.decode(errors='replace')}")
    if not raw:
        return headers, None
    return headers, json.loads(raw)


def tail(path, limit=12000):
    try:
        data = path.read_bytes()
        return data[-limit:].decode(errors="replace")
    except FileNotFoundError:
        return "<missing>"


def dump_logs():
    print("\n--- control.log ---\n" + tail(TMP / "control.log"), file=sys.stderr)
    for log in (TMP / "sandboxes").glob("*/wsd.log"):
        print(f"\n--- {log} ---\n{tail(log)}", file=sys.stderr)


def stop_processes():
    global CONTROL, CONTROL_LOG
    if CONTROL is not None and CONTROL.poll() is None:
        try:
            os.killpg(CONTROL.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            CONTROL.wait(timeout=25)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(CONTROL.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            CONTROL.wait(timeout=5)
    if CONTROL_LOG is not None:
        CONTROL_LOG.close()
        CONTROL_LOG = None
    for pid_file in (TMP / "sandboxes").glob("*/wsd.pid"):
        try:
            pid = int(pid_file.read_text().strip())
            os.killpg(pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError, ValueError, FileNotFoundError):
            pass


def consume_sse(conv_id, run_id, cookie):
    conn = http.client.HTTPConnection("127.0.0.1", PORTS["api"], timeout=210)
    conn.request("GET", f"/api/conversations/{conv_id}/events?after=0", headers={"Cookie": cookie})
    response = conn.getresponse()
    if response.status != 200:
        raw = response.read().decode(errors="replace")
        raise RuntimeError(f"SSE status={response.status}: {raw}")
    event_name = ""
    event_id = ""
    data_lines = []
    persistent = 0
    temporary = 0
    terminal = ""
    terminal_data = None
    deadline = time.monotonic() + 200
    try:
        while time.monotonic() < deadline and not terminal:
            line = response.readline()
            if not line:
                raise RuntimeError("SSE ended before a terminal Run event")
            line = line.decode().rstrip("\r\n")
            if line == "":
                if not data_lines:
                    event_name = ""
                    event_id = ""
                    continue
                frame = json.loads("\n".join(data_lines))
                if event_name == "ui":
                    persistent += 1
                    if not event_id:
                        raise RuntimeError("persistent SSE frame has no id")
                elif event_name == "temp":
                    temporary += 1
                    if event_id:
                        raise RuntimeError("temporary SSE frame unexpectedly has an id")
                for item in frame.get("items", []):
                    if item.get("type") in ("run_finished", "run_failed", "run_cancelled"):
                        if frame.get("run_id") == run_id:
                            terminal = item["type"]
                            terminal_data = item.get("data")
                event_name = ""
                event_id = ""
                data_lines = []
                continue
            if line.startswith(":"):
                continue
            if line.startswith("event:"):
                event_name = line[6:].strip()
            elif line.startswith("id:"):
                event_id = line[3:].strip()
            elif line.startswith("data:"):
                data_lines.append(line[5:].lstrip())
        if not terminal:
            raise RuntimeError("timed out waiting for terminal SSE event")
        return persistent, temporary, terminal, terminal_data
    finally:
        response.close()
        conn.close()


def main():
    global CONTROL, CONTROL_LOG, PORTS
    probe = socket.socket()
    try:
        probe.bind(("127.0.0.1", 3080))
    except OSError as exc:
        raise RuntimeError("local acceptance requires the current wsd fixed dshd port 127.0.0.1:3080") from exc
    finally:
        probe.close()
    api_port, preview_port, internal_port, metrics_port = free_ports(4)
    PORTS = {"api": api_port, "preview": preview_port, "internal": internal_port, "metrics": metrics_port}
    (TMP / "bin").mkdir()
    print("building control, wsd and dshd...", flush=True)
    run(["go", "build", "-o", str(TMP / "bin" / "control"), "./cmd/control"], timeout=180)
    run(["go", "build", "-o", str(TMP / "bin" / "wsd"), "./cmd/wsd"], timeout=180)
    run(["go", "build", "-o", str(TMP / "bin" / "dshd"), "./cmd/dshd"], cwd=ROOT / "dsh-go", timeout=180)

    def q(path):
        return json.dumps(str(path))

    config = f"""mode: local
listen:
  api: 127.0.0.1:{api_port}
  preview: 127.0.0.1:{preview_port}
  internal: 127.0.0.1:{internal_port}
  metrics: 127.0.0.1:{metrics_port}
web:
  origin: http://127.0.0.1:{api_port}
control:
  secret_file: {q(TMP / 'data' / 'secret.key')}
sqlite:
  path: {q(TMP / 'data' / 'control.db')}
auth:
  cookie_secure: false
  argon2:
    time: 1
    memory: 1024
    threads: 1
catalog:
  file: {q(DEPLOY / 'models.local.yaml')}
sandbox:
  driver: local
  internal_url: http://127.0.0.1:{internal_port}
  start_timeout: 45s
local:
  root: {q(TMP / 'sandboxes')}
  wsd_bin: {q(TMP / 'bin' / 'wsd')}
  template_dir: {q(ROOT / 'image' / 'templates' / 'react-go-sqlite')}
  dshd_bin: {q(TMP / 'bin' / 'dshd')}
  image_ver: local-e2e
run:
  max_duration: 180s
  stop_confirm_timeout: 10s
  reconcile_interval: 100ms
preview:
  domain: localhost
  scheme: http
  public_address: '{{key}}.preview.localhost:{preview_port}'
  cookie_secure: false
  build_timeout: 120s
agentclient:
  preview_timeout: 130s
"""
    config_path = TMP / "control.yaml"
    config_path.write_text(config)
    run([str(TMP / "bin" / "control"), "user", "add", "-config", str(config_path), "-user", "alice", "-password", "local-e2e-password"])
    CONTROL_LOG = open(TMP / "control.log", "wb")
    CONTROL = subprocess.Popen([str(TMP / "bin" / "control"), "serve", "-config", str(config_path)], cwd=ROOT, env=ENV, stdout=CONTROL_LOG, stderr=subprocess.STDOUT, start_new_session=True)

    deadline = time.monotonic() + 35
    cookie = ""
    while time.monotonic() < deadline:
        if CONTROL.poll() is not None:
            raise RuntimeError(f"control exited early with code {CONTROL.returncode}")
        try:
            headers, _ = api("POST", "/api/login", {"username": "alice", "password": "local-e2e-password"}, expected=204, timeout=2)
            cookie = headers.get("Set-Cookie").split(";", 1)[0]
            break
        except (OSError, RuntimeError):
            time.sleep(0.15)
    if not cookie:
        raise RuntimeError("control did not become ready")
    _, models = api("GET", "/api/models", cookie=cookie)
    assert models["items"][0]["id"] == "local-demo"
    _, conversation = api("POST", "/api/conversations", {"title": "local e2e"}, cookie=cookie, expected=201)
    conv_id = conversation["id"]
    _, accepted = api("POST", f"/api/conversations/{conv_id}/messages", {"client_request_id": "local-e2e-1", "content": "构建本地演示应用"}, cookie=cookie, expected=202)
    run_id = accepted["run_id"]
    print(f"Run {run_id} accepted; waiting for SSE terminal event...", flush=True)
    persistent, temporary, terminal, terminal_data = consume_sse(conv_id, run_id, cookie)
    _, run_view = api("GET", f"/api/conversations/{conv_id}/runs/{run_id}", cookie=cookie)
    if terminal != "run_finished" or run_view["status"] != "finished":
        _, history = api("GET", f"/api/conversations/{conv_id}/events/history?limit=200", cookie=cookie)
        raise RuntimeError(f"Run failed: terminal={terminal}, data={terminal_data}, run={run_view}, history={history}")
    _, messages = api("GET", f"/api/conversations/{conv_id}/messages", cookie=cookie)
    roles = [item["role"] for item in messages["items"]]
    if "assistant" not in roles or "user" not in roles:
        raise RuntimeError(f"messages missing roles: {messages}")
    _, tree = api("GET", f"/api/conversations/{conv_id}/files/tree?path=", cookie=cookie, timeout=30)
    if not tree:
        raise RuntimeError("workspace tree is empty")
    _, content = api("GET", f"/api/conversations/{conv_id}/files/content?path=web/src/App.tsx", cookie=cookie)
    if "local demo" not in content["content"] and "本地 demo" not in content["content"]:
        raise RuntimeError("demo write_file content was not observed")

    print("Run and files passed; ensuring preview...", flush=True)
    _, preview = api("GET", f"/api/conversations/{conv_id}/preview", cookie=cookie, timeout=145)
    parsed = urllib.parse.urlsplit(preview["url"])
    host = parsed.netloc
    ticket = urllib.parse.parse_qs(parsed.query)["t"][0]
    status, headers, raw = request(PORTS["preview"], "GET", "/__enter?t=" + urllib.parse.quote(ticket), host=host, timeout=30)
    if status != 302:
        raise RuntimeError(f"preview enter status={status}: {raw.decode(errors='replace')}")
    preview_cookie = headers.get("Set-Cookie").split(";", 1)[0]
    status, _, raw = request(PORTS["preview"], "GET", "/api/v1/health", cookie=preview_cookie, host=host, timeout=30)
    if status != 200 or not json.loads(raw).get("ok"):
        raise RuntimeError(f"preview health status={status}: {raw.decode(errors='replace')}")
    status, _, raw = request(PORTS["preview"], "GET", "/", cookie=preview_cookie, host=host, timeout=30)
    if status != 200 or b"<html" not in raw.lower():
        raise RuntimeError(f"preview root status={status}: {raw[:500]!r}")

    usage_raw = run([str(TMP / "bin" / "control"), "usage", "-config", str(config_path), "-user", "alice", "-from", "2020-01-01"], timeout=30)
    usage = json.loads(usage_raw)
    if not usage["items"]:
        raise RuntimeError("demo LLM usage was not recorded")
    _, status_view = request(PORTS["metrics"], "GET", "/debug/status")
    print(json.dumps({
        "result": "PASS",
        "conversation_id": conv_id,
        "run_id": run_id,
        "run_status": run_view["status"],
        "commit": run_view["commit"],
        "sse_persistent_frames": persistent,
        "sse_temporary_frames": temporary,
        "workspace_entries": len(tree),
        "preview_status": preview["status"],
        "preview_health": True,
        "usage_records": len(usage["items"]),
        "metrics_status": status_view["mode"],
    }, ensure_ascii=False, indent=2), flush=True)


try:
    main()
except Exception:
    dump_logs()
    raise
finally:
    stop_processes()
    shutil.rmtree(TMP, ignore_errors=True)
