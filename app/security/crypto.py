"""凭据加密: AES-256-GCM(带随机 nonce), 密文以 hex 落盘。

格式: hex(nonce(12) || ciphertext || tag)
"""
from __future__ import annotations

import os

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from .. import config

_NONCE_LEN = 12


def encrypt_credential(plaintext: str, key: bytes | None = None) -> str:
    key = key or config.secret_key()
    nonce = os.urandom(_NONCE_LEN)
    ct = AESGCM(key).encrypt(nonce, plaintext.encode("utf-8"), None)
    return (nonce + ct).hex()


def decrypt_credential(payload: str, key: bytes | None = None) -> str:
    key = key or config.secret_key()
    raw = bytes.fromhex(payload)
    nonce, ct = raw[:_NONCE_LEN], raw[_NONCE_LEN:]
    plain = AESGCM(key).decrypt(nonce, ct, None)
    return plain.decode("utf-8")
