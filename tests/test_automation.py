"""Webhook 联动测试: 规则 CRUD / 触发执行动作链 / 延迟 / 未知 token / Emby 刷新。"""
from __future__ import annotations

import pytest

from app.services.automation import AutomationService


class TestAutomation:
    def test_rule_crud(self):
        svc = AutomationService(action_runner=lambda a, b: None)
        rule = svc.create_rule("qas", "qas_strm", ["strm_scan:1", "emby_refresh"])
        assert rule.token.startswith("whk_")

        rules = svc.list_rules()
        assert any(r["name"] == "qas" for r in rules)
        assert svc.delete_rule(rule.id) is True
        assert svc.delete_rule(rule.id) is False  # 已删除

    def test_duplicate_name(self):
        svc = AutomationService()
        svc.create_rule("dup", "webhook", [])
        with pytest.raises(ValueError):
            svc.create_rule("dup", "webhook", [])

    def test_trigger_runs_chain_in_order(self):
        calls = []
        svc = AutomationService(action_runner=lambda a, b: calls.append(a))
        rule = svc.create_rule("chain", "webhook",
                               ["a_action", "b_action", "c_action"])
        result = svc.trigger(rule.token, {})
        assert calls == ["a_action", "b_action", "c_action"]
        assert all(r["ok"] for r in result["results"])

    def test_trigger_unknown_token(self):
        svc = AutomationService(action_runner=lambda a, b: None)
        with pytest.raises(KeyError):
            svc.trigger("whk_nope", {})

    def test_trigger_delay(self):
        import time
        calls = []
        svc = AutomationService(action_runner=lambda a, b: calls.append(a))
        rule = svc.create_rule("delay", "webhook", ["x"])
        t0 = time.monotonic()
        svc.trigger(rule.token, {"delayTime": 0.2})
        assert time.monotonic() - t0 >= 0.15
        assert calls == ["x"]

    def test_trigger_partial_failure_reported(self):
        def runner(action, body):
            if action == "bad":
                raise RuntimeError("boom")
        svc = AutomationService(action_runner=runner)
        rule = svc.create_rule("fail", "webhook", ["ok", "bad"])
        result = svc.trigger(rule.token, {})
        assert result["results"][0]["ok"] is True
        assert result["results"][1]["ok"] is False
        assert "boom" in result["results"][1]["error"]

    def test_emby_refresh_requires_config(self):
        svc = AutomationService(action_runner=None)
        with pytest.raises(RuntimeError, match="EMBY_HOST"):
            svc._emby_refresh()

    def test_unknown_action_kind(self):
        svc = AutomationService(action_runner=None)
        with pytest.raises(ValueError, match="未知动作"):
            svc._run_action("nonsense", None)
