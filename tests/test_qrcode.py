"""115 扫码登录服务测试: fake client 注入验证流程与状态机。"""
from __future__ import annotations

import pytest

from app.services.qrcode import QrcodeLoginService


class FakeP115Client:
    """模拟 p115client 扫码接口。"""

    def __init__(self, scan_results=None, cookies=None):
        self.scan_results = scan_results or [{"data": {"status": 0}}]
        self._cookies = cookies or {"UID": "123", "CID": "456"}
        self.scan_calls = 0

    def login_qrcode_token(self):
        return ("QRTOKEN1", "QRUID1")

    def login_qrcode_scan(self, qr_token, qr_uid):
        idx = min(self.scan_calls, len(self.scan_results) - 1)
        self.scan_calls += 1
        return self.scan_results[idx]

    @property
    def cookies(self):
        return dict(self._cookies)


class TestQrcodeLogin:
    def test_start(self):
        svc = QrcodeLoginService()
        result = svc.start("p115", client=FakeP115Client())
        assert result["qr_token"] == "QRTOKEN1"
        assert result["qr_uid"] == "QRUID1"
        assert "qrcodeapi.115.com" in result["image_url"]
        assert "QRTOKEN1" in result["image_url"]

    def test_start_waiting_then_confirmed(self):
        svc = QrcodeLoginService()
        client = FakeP115Client(scan_results=[
            {"data": {"status": 0}},   # 等待
            {"data": {"status": 1}},   # 已扫码
            {"data": {"status": 2}},   # 确认成功
        ])
        r1 = svc.poll("p115", "T", "U", client=client)
        assert r1["status"] == "waiting"
        r2 = svc.poll("p115", "T", "U", client=client)
        assert r2["status"] == "scanned"
        r3 = svc.poll("p115", "T", "U", client=client)
        assert r3["status"] == "confirmed"
        assert "UID=123" in r3["cookies"]
        assert "CID=456" in r3["cookies"]

    def test_expired(self):
        svc = QrcodeLoginService()
        client = FakeP115Client(scan_results=[{"data": {"status": -2}}])
        assert svc.poll("p115", "T", "U", client=client)["status"] == "expired"

    def test_unsupported_driver(self):
        svc = QrcodeLoginService()
        with pytest.raises(ValueError, match="不支持扫码"):
            svc.start("local")
        with pytest.raises(ValueError, match="不支持扫码"):
            svc.poll("local", "T", "U")

    def test_missing_dependency_hint(self):
        import sys
        saved = sys.modules.get("p115client")
        sys.modules["p115client"] = None
        try:
            with pytest.raises(RuntimeError, match="p115client"):
                QrcodeLoginService().start("p115")
        finally:
            if saved is not None:
                sys.modules["p115client"] = saved
            else:
                sys.modules.pop("p115client", None)

    def test_start_dict_return_form(self):
        """兼容 p115client 返回 dict 形态的版本。"""
        class DictClient(FakeP115Client):
            def login_qrcode_token(self):
                return {"qr_token": "DT", "qr_uid": "DU"}

        svc = QrcodeLoginService()
        result = svc.start("p115", client=DictClient())
        assert result["qr_token"] == "DT" and result["qr_uid"] == "DU"
