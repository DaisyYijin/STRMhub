"""认证: 密码登录 + JWT Bearer 鉴权 + 登录失败限速。

安全基线: 密码 bcrypt 哈希校验(直接用 bcrypt 库, 不依赖 passlib ——
passlib 1.7.4 与 bcrypt>=4.1 不兼容会导致 500); 同一 IP 失败 5 次后锁定 5 分钟。
"""
from __future__ import annotations

import threading
import time

import bcrypt
from fastapi import Depends, HTTPException, Request, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from .. import config
from ..security.jwt import create_token, verify_token

_bearer = HTTPBearer(auto_error=False)

# 登录失败限速: ip -> (fail_count, lock_until)
_failures: dict[str, list] = {}
_fail_lock = threading.Lock()
MAX_FAILS = 5
LOCK_SECONDS = 300

_hashed_password: bytes | None = None


def _password_hash() -> bytes:
    global _hashed_password
    if _hashed_password is None:
        _hashed_password = bcrypt.hashpw(
            config.admin_password().encode("utf-8"), bcrypt.gensalt())
    return _hashed_password


def check_login(password: str, request: Request) -> str:
    """校验密码, 成功返回 JWT; 失败抛 401(带限速)。"""
    ip = request.client.host if request.client else "?"
    now = time.time()
    with _fail_lock:
        rec = _failures.get(ip, [0, 0.0])
        if rec[1] > now:
            raise HTTPException(status_code=429,
                                detail="尝试次数过多, 请稍后再试")
        _failures[ip] = rec

    if bcrypt.checkpw(password.encode("utf-8"), _password_hash()):
        with _fail_lock:
            _failures[ip] = [0, 0.0]
        return create_token("admin")

    with _fail_lock:
        rec = _failures.get(ip, [0, 0.0])
        rec[0] += 1
        if rec[0] >= MAX_FAILS:
            rec[1] = now + LOCK_SECONDS
            rec[0] = 0
        _failures[ip] = rec
    raise HTTPException(status_code=401, detail="密码错误")


def require_user(cred: HTTPAuthorizationCredentials | None = Depends(_bearer)) -> str:
    """JWT Bearer 鉴权依赖。"""
    if cred is None:
        raise HTTPException(status_code=401, detail="未提供凭据")
    payload = verify_token(cred.credentials)
    if payload is None:
        raise HTTPException(status_code=401, detail="凭据无效或已过期")
    return payload.get("sub", "admin")
