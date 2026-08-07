"""115 扫码登录服务测试(p115client 0.0.9.x 新 API): fake 客户端注入验证流程与状态机。"""
from __future__ import annotations

import pytest

from app.services.qrcode import QrcodeLoginService


class FakeP115Client:
    """模拟 p115client 0.0.9.x 扫码接口(类方法形态)。"""

    scan_results = [{"data": {"status": 0}}]
    scan_calls = 0
    cookies = {"UID": "123", "CID": "456"}

    @classmethod
    def login_qrcode_token(cls):
        return {"data": {"uid": "U1", "time": "T1", "sign": "S1",
                         "qrcode": "https://115.com/scan/dg-U1"}}

    @classmethod
    def login_qrcode_scan_status(cls, payload):
        idx = min(cls.scan_calls, len(cls.scan_results) - 1)
        cls.scan_calls += 1
        return cls.scan_results[idx]

    @classmethod
    def login_qrcode_scan_result(cls, uid, app="web"):
        assert app  # 设备参数必须传递
        return {"data": {"cookie": cls.cookies}}

    def __init__(self, credential=""):
        self.credential = credential

    def user_my_info(self):
        return {"data": {"uname": "测试用户",
                          "vip": {"is_vip": True, "is_forever": True},
                          "face": {"face_s": "https://img.example.com/a.png"}}}

    def fs_index_info(self, payload=0):
        return {"data": {"space_info": {
            "all_total": {"size": 10 * 2 ** 30, "size_format": "10.0 GB"},
            "all_use": {"size": 2 * 2 ** 30, "size_format": "2.0 GB"},
            "all_remain": {"size": 8 * 2 ** 30, "size_format": "8.0 GB"}}}}


@pytest.fixture(autouse=True)
def reset_fake():
    FakeP115Client.scan_calls = 0
    yield
    FakeP115Client.scan_calls = 0


class TestQrcodeLogin:
    def test_start(self):
        svc = QrcodeLoginService(client_cls=FakeP115Client)
        result = svc.start("p115")
        assert result["uid"] == "U1"
        assert result["time"] == "T1"
        assert result["sign"] == "S1"
        assert result["qr_image"].startswith("data:image/svg+xml;base64,")
        assert any(a["key"] == "web" for a in result["apps"])  # 设备列表

    def test_poll_waiting_then_confirmed(self):
        FakeP115Client.scan_results = [
            {"data": {"status": 0}},   # 等待
            {"data": {"status": 1}},   # 已扫码待确认
            {"data": {"status": 2}},   # 确认成功
        ]
        svc = QrcodeLoginService(client_cls=FakeP115Client)
        assert svc.poll("p115", "U1", "T1", "S1", "android")["status"] == "waiting"
        assert svc.poll("p115", "U1", "T1", "S1", "android")["status"] == "scanned"
        r = svc.poll("p115", "U1", "T1", "S1", "android")
        assert r["status"] == "confirmed"
        assert "UID=123" in r["cookies"]
        assert "CID=456" in r["cookies"]

    def test_poll_expired_and_cancelled(self):
        FakeP115Client.scan_results = [{"data": {"status": -1}}]
        svc = QrcodeLoginService(client_cls=FakeP115Client)
        assert svc.poll("p115", "U", "T", "S")["status"] == "expired"
        FakeP115Client.scan_results = [{"data": {"status": -2}}]
        assert svc.poll("p115", "U", "T", "S")["status"] == "cancelled"

    def test_unsupported_driver(self):
        svc = QrcodeLoginService()
        with pytest.raises(ValueError, match="不支持扫码"):
            svc.start("local")
        with pytest.raises(ValueError, match="不支持扫码"):
            svc.poll("local", "U", "T", "S")

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

    def test_token_missing_uid_raises(self):
        class BadClient(FakeP115Client):
            @classmethod
            def login_qrcode_token(cls):
                return {"data": {}}

        svc = QrcodeLoginService(client_cls=BadClient)
        with pytest.raises(RuntimeError, match="获取二维码失败"):
            svc.start("p115")

    def test_fetch_account_info(self):
        """扫码确认后拉取账号信息: 昵称/VIP/头像/容量。"""
        svc = QrcodeLoginService(client_cls=FakeP115Client)
        info = svc.fetch_account_info("p115", "UID=123; CID=456")
        assert info["nickname"] == "测试用户"
        assert info["vip"] == "永久 VIP"
        assert info["avatar"] == "https://img.example.com/a.png"
        assert info["total_size"] == 10 * 2 ** 30
        assert info["total_size_fmt"] == "10.0 GB"
        assert info["used_size_fmt"] == "2.0 GB"
        # 非 115 驱动返回空
        assert svc.fetch_account_info("local", "x") == {}

    def test_fetch_account_info_graceful_failure(self):
        """信息接口抛异常时返回 {} 不影响登录。"""
        class BrokenClient(FakeP115Client):
            def user_my_info(self):
                raise RuntimeError("boom")

            def fs_index_info(self, payload=0):
                raise RuntimeError("boom2")

        svc = QrcodeLoginService(client_cls=BrokenClient)
        assert svc.fetch_account_info("p115", "x") == {}

    def test_time_sign_int_normalized_to_str(self):
        """115 返回 int 时间戳时, start 必须输出字符串(否则 poll 422)。"""
        class IntFieldsClient(FakeP115Client):
            @classmethod
            def login_qrcode_token(cls):
                return {"data": {"uid": "U7", "time": 1730000000,
                                 "sign": "abc", "qrcode": "x"}}

        svc = QrcodeLoginService(client_cls=IntFieldsClient)
        result = svc.start("p115")
        assert result["uid"] == "U7"
        assert result["time"] == "1730000000"
        assert result["sign"] == "abc"
        assert isinstance(result["time"], str)

    def test_qrcode_empty_fallback_url(self):
        class NoQrcodeClient(FakeP115Client):
            @classmethod
            def login_qrcode_token(cls):
                return {"data": {"uid": "U9", "time": "T", "sign": "S",
                                 "qrcode": ""}}

        svc = QrcodeLoginService(client_cls=NoQrcodeClient)
        result = svc.start("p115")
        assert result["uid"] == "U9"
        # qr_image 是 base64 编码的 SVG(二维码内容含兜底 URL https://115.com/scan/dg-U9)
        import base64
        svg = base64.b64decode(result["qr_image"].split(",", 1)[1]).decode("utf-8")
        assert "<svg" in svg


class TestQrcodeApiAutoCreate:
    """API 集成: poll confirmed 时自动创建账户(monkeypatch 服务层)。"""

    def test_poll_confirmed_auto_creates_account(self, monkeypatch):
        from fastapi.testclient import TestClient
        from app.main import app

        def fake_poll(driver_type, uid, time, sign, app="web"):
            return {"status": "confirmed",
                    "cookies": "UID=123; CID=456"}

        def fake_info(driver_type, credential):
            return {"nickname": "扫码用户", "vip": "VIP",
                    "total_size_fmt": "10.0 GB"}

        monkeypatch.setattr("app.api.qrcode.qrcode_login.poll", fake_poll)
        monkeypatch.setattr(
            "app.api.qrcode.qrcode_login.fetch_account_info", fake_info)
        with TestClient(app) as c:
            token = c.post("/api/auth/login",
                           json={"password": "testpass"}).json()["token"]
            h = {"Authorization": f"Bearer {token}"}
            r = c.post("/api/accounts/qrcode/poll", headers=h, json={
                "driver_type": "p115", "uid": "U", "time": "T",
                "sign": "S", "app": "android"})
            assert r.status_code == 200, r.text
            body = r.json()
            assert body["status"] == "confirmed"
            assert body["account"]["name"] == "扫码用户"
            assert body["account"]["driver_type"] == "p115"
            assert body["account"]["info"]["vip"] == "VIP"
            assert body["account"]["info"]["device"] == "android"
            # 列表中出现自动创建的账户
            accounts = c.get("/api/accounts", headers=h).json()
            assert any(a["name"] == "扫码用户" for a in accounts)
