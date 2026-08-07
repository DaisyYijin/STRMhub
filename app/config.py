"""全局配置: 数据目录 / 密钥 / 默认管理员。

约定:
- 数据目录默认 <项目根>/data, 可用环境变量 STRMHUB_DATA 覆盖。
- 凭据加密密钥: 首次启动生成随机 32 字节, 持久化到 data/secret.key。
- 管理员密码: 环境变量 STRMHUB_ADMIN_PASSWORD(测试/部署注入),
  未设置时使用默认值 admin(生产环境务必修改)。
"""
from __future__ import annotations

import os
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parent.parent


def data_dir() -> Path:
    d = os.environ.get("STRMHUB_DATA")
    return Path(d) if d else PROJECT_ROOT / "data"


def ensure_dirs() -> None:
    for sub in ("", "db", "strm"):
        (data_dir() / sub).mkdir(parents=True, exist_ok=True)


def secret_key() -> bytes:
    """读取或生成并持久化 32 字节主密钥(用于凭据 AES-GCM 加密)。"""
    key_file = data_dir() / "secret.key"
    if key_file.exists():
        return key_file.read_bytes()
    import secrets
    key = secrets.token_bytes(32)
    key_file.write_bytes(key)
    return key


def admin_password() -> str:
    return os.environ.get("STRMHUB_ADMIN_PASSWORD", "admin")


def jwt_secret() -> str:
    """JWT 签名密钥: 与主密钥同源(HS256 需要字节串)。"""
    return secret_key().hex()


# ---- 端口配置(用户指定) ----
ADMIN_PORT = 6060      # 管理页面/API
PROXY_PORT = 6086      # Emby 302 反代
