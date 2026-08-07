"""安全模块测试: 凭据加密往返/防篡改, JWT 签发/验证/过期/篡改。"""
from __future__ import annotations

import time

import pytest

from app.security import crypto, jwt


class TestCrypto:
    def test_roundtrip(self):
        plain = "115_cookie=abc; uid=123"
        enc = crypto.encrypt_credential(plain)
        assert enc != plain
        assert crypto.decrypt_credential(enc) == plain

    def test_random_nonce(self):
        enc1 = crypto.encrypt_credential("same")
        enc2 = crypto.encrypt_credential("same")
        assert enc1 != enc2  # 随机 nonce -> 同明文不同密文

    def test_tamper_fails(self):
        enc = crypto.encrypt_credential("secret")
        tampered = enc[:-2] + ("00" if not enc.endswith("00") else "11")
        with pytest.raises(Exception):
            crypto.decrypt_credential(tampered)

    def test_empty(self):
        enc = crypto.encrypt_credential("")
        assert crypto.decrypt_credential(enc) == ""


class TestJwt:
    def test_create_and_verify(self):
        token = jwt.create_token("admin", ttl_seconds=3600)
        payload = jwt.verify_token(token)
        assert payload is not None
        assert payload["sub"] == "admin"

    def test_expired(self):
        token = jwt.create_token("admin", ttl_seconds=-10)
        assert jwt.verify_token(token) is None

    def test_tampered_signature(self):
        token = jwt.create_token("admin")
        head, pay, sig = token.split(".")
        bad = f"{head}.{pay}.{sig[:-1]}{'A' if sig[-1] != 'A' else 'B'}"
        assert jwt.verify_token(bad) is None

    def test_wrong_secret(self):
        token = jwt.create_token("admin", secret="key-a")
        assert jwt.verify_token(token, secret="key-b") is None

    def test_garbage(self):
        assert jwt.verify_token("not-a-jwt") is None
        assert jwt.verify_token("a.b") is None
