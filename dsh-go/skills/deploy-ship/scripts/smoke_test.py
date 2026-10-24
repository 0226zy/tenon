#!/usr/bin/env python3
"""smoke_test.py — 上线后冒烟测试（零依赖，仅标准库 urllib）。

用法:
    python3 smoke_test.py <base_url>
    python3 smoke_test.py <base_url> --register /api/v1/auth/register \
        --login /api/v1/auth/login --protected /api/v1/users/me

行为:
    1. 始终检查 GET /health 期望 200（liveness，不依赖 DB）。
    2. 提供 --register/--login/--protected 时跑业务旅程:
       注册(随机账号) → 登录(取 token 或 session cookie) → 带凭证访问受保护端点。
       登录响应支持两种凭证形态:
         - JSON body 中含 access_token / token / session_id / sessionId → Authorization: Bearer
         - Set-Cookie 会话 → urllib cookiejar 自动携带
    3. 注册返回 409/400(已存在) 时视为可继续（直接复用该账号登录）。

退出码: 任一步骤失败 exit 1；全部通过 exit 0。
"""

import argparse
import json
import secrets
import sys
import urllib.error
import urllib.request
from http.cookiejar import CookieJar

TOKEN_KEYS = ("access_token", "token", "session_id", "sessionId", "id_token")


class Smoke:
    def __init__(self, base_url: str, timeout: float):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.failures = 0
        # 支持 Set-Cookie 会话的 opener
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(CookieJar())
        )

    def _request(self, method: str, path: str, body=None, headers=None):
        url = path if path.startswith("http") else self.base_url + path
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Accept", "application/json")
        if body is not None:
            req.add_header("Content-Type", "application/json")
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        try:
            with self.opener.open(req, timeout=self.timeout) as resp:
                raw = resp.read().decode("utf-8", errors="replace")
                return resp.status, raw
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", errors="replace")
            return exc.code, raw
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            return None, str(exc)

    def check(self, name: str, ok: bool, detail: str = ""):
        status = "PASS" if ok else "FAIL"
        print(f"[{status}] {name}" + (f" —— {detail}" if detail else ""))
        if not ok:
            self.failures += 1
        return ok

    def step_health(self, path: str):
        code, raw = self._request("GET", path)
        self.check(
            f"GET {path} 期望 200",
            code == 200,
            f"实际状态码: {code}" + (f"，响应: {raw[:120]}" if code != 200 and raw else ""),
        )

    def step_register(self, path: str, email: str, password: str) -> bool:
        code, raw = self._request("POST", path, {"email": email, "password": password})
        ok = code in (200, 201)
        if code in (400, 409, 422) and "exist" in raw.lower() or code in (409,):
            # 账号已存在：可继续用同账号走登录旅程
            self.check(f"POST {path} 注册", True, f"账号已存在（{code}），继续登录旅程")
            return True
        return self.check(
            f"POST {path} 注册期望 200/201",
            ok,
            f"实际状态码: {code}，响应: {raw[:160]}",
        )

    def step_login(self, path: str, email: str, password: str) -> str | None:
        code, raw = self._request("POST", path, {"email": email, "password": password})
        if not self.check(
            f"POST {path} 登录期望 200",
            code == 200,
            f"实际状态码: {code}，响应: {raw[:160]}",
        ):
            return None
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            payload = {}
        for key in TOKEN_KEYS:
            if isinstance(payload, dict) and payload.get(key):
                return str(payload[key])
        return None  # 无 token：可能走 Set-Cookie 会话（cookiejar 已自动保存）

    def step_protected(self, path: str, token: str | None):
        headers = {"Authorization": f"Bearer {token}"} if token else {}
        code, raw = self._request("GET", path, headers=headers)
        via = "Bearer token" if token else "session cookie"
        self.check(
            f"GET {path} 带凭证({via}) 期望 200",
            code == 200,
            f"实际状态码: {code}，响应: {raw[:160]}",
        )
        # 反向校验：无凭证访问必须被拒
        opener = urllib.request.build_opener()  # 干净 opener，不带 cookie
        req = urllib.request.Request(self.base_url + path)
        try:
            with opener.open(req, timeout=self.timeout) as resp:
                anon_code = resp.status
        except urllib.error.HTTPError as exc:
            anon_code = exc.code
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            anon_code = None
            raw = str(exc)
        self.check(
            f"GET {path} 无凭证期望 401/403（默认拒绝）",
            anon_code in (401, 403),
            f"实际状态码: {anon_code}",
        )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="上线冒烟：/health + 可选 注册→登录→受保护端点 业务旅程"
    )
    parser.add_argument("base_url", help="如 http://127.0.0.1:8000 或 https://app.example.com")
    parser.add_argument("--health", default="/health", help="liveness 路径，默认 /health")
    parser.add_argument("--register", help="注册端点路径，如 /api/v1/auth/register")
    parser.add_argument("--login", help="登录端点路径，如 /api/v1/auth/login")
    parser.add_argument("--protected", help="受保护端点路径，如 /api/v1/users/me")
    parser.add_argument("--email", help="旅程账号邮箱（默认随机生成）")
    parser.add_argument("--password", default=None, help="旅程账号密码（默认随机生成）")
    parser.add_argument("--timeout", type=float, default=10.0, help="单请求超时秒数")
    args = parser.parse_args()

    smoke = Smoke(args.base_url, args.timeout)
    print(f"冒烟目标: {smoke.base_url}")

    smoke.step_health(args.health)

    if args.register or args.login or args.protected:
        if not (args.register and args.login and args.protected):
            print("[FAIL] --register/--login/--protected 必须三个一起提供")
            return 1
        email = args.email or f"smoke-{secrets.token_hex(4)}@example.com"
        password = args.password or "Smoke!" + secrets.token_hex(6)
        if smoke.step_register(args.register, email, password):
            token = smoke.step_login(args.login, email, password)
            # 登录失败时 step_login 已记 FAIL，仍尝试受保护端点以暴露更多线索
            smoke.step_protected(args.protected, token)

    print()
    if smoke.failures:
        print(f"冒烟失败: {smoke.failures} 项未通过。上线门禁未达成。")
        return 1
    print("冒烟全部通过。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
