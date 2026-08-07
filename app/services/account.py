"""账户服务: 凭据加密存取 + 驱动实例化 + 脱敏输出。

安全基线: 凭据 AES-GCM 加密落盘(decrypt 仅在驱动实例化时进行),
对外 API 一律脱敏(不返回 credential_enc)。
"""
from __future__ import annotations

import json

from sqlalchemy import select

from ..db.models import Account
from ..db.session import session_scope
from ..drivers import registry
from ..security.crypto import decrypt_credential, encrypt_credential


class AccountService:
    def create(self, name: str, driver_type: str, credential: str = "",
               config: dict | None = None, info: dict | None = None) -> Account:
        if not registry.get_meta(driver_type):
            raise KeyError(f"未知驱动类型: {driver_type}")
        with session_scope() as s:
            exists = s.scalar(select(Account).where(Account.name == name))
            if exists:
                raise ValueError(f"账户名已存在: {name}")
            acc = Account(
                name=name,
                driver_type=driver_type,
                credential_enc=encrypt_credential(credential) if credential else "",
                config_json=json.dumps(config or {}, ensure_ascii=False),
                info_json=json.dumps(info or {}, ensure_ascii=False),
            )
            s.add(acc)
            s.flush()
            s.refresh(acc)
            return acc

    def get(self, account_id: int) -> Account | None:
        with session_scope() as s:
            return s.get(Account, account_id)

    def list(self) -> list[Account]:
        with session_scope() as s:
            return list(s.scalars(select(Account).order_by(Account.id)))

    def delete(self, account_id: int) -> bool:
        with session_scope() as s:
            acc = s.get(Account, account_id)
            if acc is None:
                return False
            s.delete(acc)
            return True

    def driver_for(self, account: Account):
        """解密凭据并实例化驱动。"""
        credential = ""
        if account.credential_enc:
            credential = decrypt_credential(account.credential_enc)
        config = json.loads(account.config_json or "{}")
        return registry.create(account.driver_type, credential=credential, config=config)

    @staticmethod
    def to_dict(acc: Account) -> dict:
        """脱敏输出: 不泄露凭据密文。"""
        return {
            "id": acc.id,
            "name": acc.name,
            "driver_type": acc.driver_type,
            "config": json.loads(acc.config_json or "{}"),
            "info": json.loads(acc.info_json or "{}"),
            "status": acc.status,
            "created_at": acc.created_at.isoformat() if acc.created_at else None,
        }
