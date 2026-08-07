"""Webhook 联动: 规则 CRUD + 触发 + 动作链执行。

设计来源: SmartStrm Webhook 生态(qas_strm/cs_strm/a_task + delayTime) +
LitePan automation(触发器 + 动作链)。
"""
from __future__ import annotations

import json
import threading
import time

from sqlalchemy import select

from ..db.models import WebhookRule
from ..db.session import session_scope


class AutomationService:
    """动作链执行器。动作类型(strm_scan/scrape/emby_refresh)由
    _run_action 分派; action_runner 可注入用于测试(签名: (action, body))。"""

    def __init__(self, action_runner=None):
        # action_runner(action, body) -> None; None 时使用内置分派
        self.action_runner = action_runner

    # ---- 规则 CRUD ----
    def create_rule(self, name: str, trigger: str, action_chain: list[str],
                    token: str = "") -> WebhookRule:
        with session_scope() as s:
            exists = s.scalar(select(WebhookRule).where(WebhookRule.name == name))
            if exists:
                raise ValueError(f"规则名已存在: {name}")
            rule = WebhookRule(
                name=name, trigger=trigger,
                action_chain_json=json.dumps(action_chain, ensure_ascii=False),
                token=token or self._gen_token(),
            )
            s.add(rule)
            s.flush()
            s.refresh(rule)
            return rule

    def list_rules(self) -> list[dict]:
        with session_scope() as s:
            rows = s.scalars(select(WebhookRule).order_by(WebhookRule.id)).all()
            return [self.to_dict(r) for r in rows]

    def delete_rule(self, rule_id: int) -> bool:
        with session_scope() as s:
            r = s.get(WebhookRule, rule_id)
            if r is None:
                return False
            s.delete(r)
            return True

    # ---- 触发 ----
    def trigger(self, token: str, body: dict | None = None) -> dict:
        """Webhook 触发: 按 token 找规则, 串行执行动作链。"""
        with session_scope() as s:
            rule = s.scalar(select(WebhookRule).where(WebhookRule.token == token))
            if rule is None or not rule.enabled:
                raise KeyError("Webhook 规则不存在或已禁用")
            chain = json.loads(rule.action_chain_json or "[]")
            delay = float((body or {}).get("delayTime", 0) or 0)

        if delay > 0:
            time.sleep(delay)

        results = []
        for action in chain:
            try:
                self._run_action(action, body)
                results.append({"action": action, "ok": True})
            except Exception as exc:
                results.append({"action": action, "ok": False, "error": str(exc)})
        return {"rule": rule.name, "results": results}

    # ---- 动作执行 ----
    def _run_action(self, action: str, body: dict | None) -> None:
        if self.action_runner is not None:
            self.action_runner(action, body)
            return
        parts = action.split(":", 1)
        kind = parts[0].strip()
        arg = parts[1].strip() if len(parts) > 1 else ""
        if kind == "strm_scan":
            from .taskmanager import TaskManager
            TaskManager().run_sync(int(arg))
        elif kind == "scrape":
            from .scrape.service import ScrapeService
            ScrapeService().run(arg, "webhook")
        elif kind == "emby_refresh":
            self._emby_refresh()
        else:
            raise ValueError(f"未知动作: {kind}")

    @staticmethod
    def _emby_refresh() -> None:
        """通知 Emby 全库刷新(EMBY_HOST/EMBY_API_KEY 环境变量)。"""
        import os

        import httpx
        host = os.environ.get("EMBY_HOST", "").rstrip("/")
        key = os.environ.get("EMBY_API_KEY", "")
        if not host or not key:
            raise RuntimeError("未配置 EMBY_HOST/EMBY_API_KEY")
        resp = httpx.post(f"{host}/Library/Refresh?api_key={key}", timeout=20)
        if resp.status_code not in (200, 204):
            raise RuntimeError(f"Emby 刷新失败: HTTP {resp.status_code}")

    @staticmethod
    def _gen_token() -> str:
        import secrets
        return "whk_" + secrets.token_hex(16)

    @staticmethod
    def to_dict(r: WebhookRule) -> dict:
        return {
            "id": r.id, "name": r.name, "trigger": r.trigger,
            "action_chain": json.loads(r.action_chain_json or "[]"),
            "enabled": r.enabled, "token": r.token,
        }


# 单例(默认 runner 为内置分派)
automation = AutomationService(action_runner=None)
