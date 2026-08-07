"""HS256 JWT: 标准库实现(hmac + base64url + json), 无第三方依赖。

payload: {"sub": username, "exp": unix_seconds, "iat": issued_at}
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import time

from .. import config

ALG = "HS256"


def _b64url_encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def _b64url_decode(s: str) -> bytes:
    pad = "=" * (-len(s) % 4)
    return base64.urlsafe_b64decode(s + pad)


def _sign(header: str, payload: str, secret: str) -> str:
    msg = f"{header}.{payload}".encode("ascii")
    return _b64url_encode(hmac.new(secret.encode(), msg, hashlib.sha256).digest())


def create_token(username: str, ttl_seconds: int = 86400,
                 secret: str | None = None) -> str:
    secret = secret or config.jwt_secret()
    header = _b64url_encode(json.dumps({"alg": ALG, "typ": "JWT"}).encode())
    now = int(time.time())
    body = {"sub": username, "iat": now, "exp": now + ttl_seconds}
    payload = _b64url_encode(json.dumps(body).encode())
    return f"{header}.{payload}.{_sign(header, payload, secret)}"


def verify_token(token: str, secret: str | None = None) -> dict | None:
    """校验签名与过期时间, 返回 payload; 任何失败返回 None。"""
    secret = secret or config.jwt_secret()
    try:
        header_b64, payload_b64, sig = token.split(".")
        expect = _sign(header_b64, payload_b64, secret)
        if not hmac.compare_digest(sig, expect):
            return None
        payload = json.loads(_b64url_decode(payload_b64))
        if payload.get("exp", 0) < time.time():
            return None
        return payload
    except (ValueError, json.JSONDecodeError, UnicodeDecodeError):
        return None
