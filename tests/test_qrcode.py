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
            # 单账号模式: 库中已有其他 p115 账户时是更新, 否则新建
            assert body["account"]["action"] in ("created", "updated")
            assert body["account"]["info"]["vip"] == "VIP"
            assert "安卓" in body["account"]["info"]["device"]  # 中文设备名
            # 列表中出现自动创建的账户
            accounts = c.get("/api/accounts", headers=h).json()
            assert any(a["name"] == "扫码用户" for a in accounts)

    def test_poll_confirmed_upserts_same_account(self, monkeypatch):
        """单账号模式: 再次扫码确认 -> 更新同一账户(action=updated), 不新增。"""
        from fastapi.testclient import TestClient
        from app.main import app

        def fake_poll(driver_type, uid, time, sign, app="web"):
            return {"status": "confirmed", "cookies": "UID=999; CID=888"}

        def fake_info(driver_type, credential):
            return {"nickname": "扫码用户", "vip": "永久 VIP",
                    "total_size_fmt": "20.0 GB"}

        monkeypatch.setattr("app.api.qrcode.qrcode_login.poll", fake_poll)
        monkeypatch.setattr(
            "app.api.qrcode.qrcode_login.fetch_account_info", fake_info)
        with TestClient(app) as c:
            token = c.post("/api/auth/login",
                           json={"password": "testpass"}).json()["token"]
            h = {"Authorization": f"Bearer {token}"}
            for _ in range(2):  # 连续两次扫码确认
                r = c.post("/api/accounts/qrcode/poll", headers=h, json={
                    "driver_type": "p115", "uid": "U", "time": "T",
                    "sign": "S", "app": "web"})
                assert r.status_code == 200, r.text
            body = r.json()
            assert body["account"]["action"] == "updated"
            accounts = c.get("/api/accounts", headers=h).json()
            # 单账号模式: 同名扫码账户只有一个(其他测试的 p115 账户不受影响)
            p115s = [a for a in accounts if a["driver_type"] == "p115"
                     and a["name"] == "扫码用户"]
            assert len(p115s) == 1  # 只有一个 115 账户


class TestAccountRules:
    """网盘账户整理规则配置 API(识别/重命名/分类/AI, 每网盘独立)。"""

    def test_save_and_get_rules(self):
        from fastapi.testclient import TestClient
        from app.main import app

        with TestClient(app) as c:
            token = c.post("/api/auth/login",
                           json={"password": "testpass"}).json()["token"]
            h = {"Authorization": f"Bearer {token}"}
            acc = c.post("/api/accounts", headers=h, json={
                "name": "规则测试", "driver_type": "local",
                "credential": ""}).json()
            aid = acc["id"]
            # 默认空规则
            assert c.get(f"/api/accounts/{aid}/rules",
                         headers=h).json()["rules"] == {}
            rules = {
                "min_video_size_mb": 100,
                "blacklist": ["trailer", "sample"],
                "custom_words": ["SW|Star Wars"],
                "custom_matches": ["星际穿越|157336|movie"],
                "release_groups": ["FRDS", "NEWCINE"],
                "rename_template": "{title}.{year}.{quality}",
                "category_rules": [{"kind": "movie", "match": "动作", "target": "动作片"}],
                "ai": {"enabled": True, "api_base": "https://x/v1",
                       "api_key": "sk-x", "model": "gpt-4o-mini"},
            }
            r = c.put(f"/api/accounts/{aid}/rules", headers=h,
                      json={"rules": rules})
            assert r.status_code == 200, r.text
            assert r.json()["rules"] == rules
            # 读回
            got = c.get(f"/api/accounts/{aid}/rules", headers=h).json()["rules"]
            assert got["min_video_size_mb"] == 100
            assert got["category_rules"][0]["target"] == "动作片"
            assert got["ai"]["model"] == "gpt-4o-mini"
            # 账户 config 同步
            mine = [a for a in c.get("/api/accounts",
                                     headers=h).json() if a["id"] == aid][0]
            assert mine["config"]["rules"]["release_groups"] == ["FRDS", "NEWCINE"]

    def test_rules_whitelist_filtered(self):
        """非白名单字段被丢弃。"""
        from fastapi.testclient import TestClient
        from app.main import app

        with TestClient(app) as c:
            token = c.post("/api/auth/login",
                           json={"password": "testpass"}).json()["token"]
            h = {"Authorization": f"Bearer {token}"}
            acc = c.post("/api/accounts", headers=h, json={
                "name": "规则测试2", "driver_type": "local",
                "credential": ""}).json()
            r = c.put(f"/api/accounts/{acc['id']}/rules", headers=h,
                      json={"rules": {"evil": "x", "rename_template": "{title}"}})
            assert r.status_code == 200
            assert "evil" not in r.json()["rules"]
            assert r.json()["rules"]["rename_template"] == "{title}"

    def test_rules_missing_account_404(self):
        from fastapi.testclient import TestClient
        from app.main import app

        with TestClient(app) as c:
            token = c.post("/api/auth/login",
                           json={"password": "testpass"}).json()["token"]
            h = {"Authorization": f"Bearer {token}"}
            assert c.get("/api/accounts/99999/rules",
                         headers=h).status_code == 404
            assert c.put("/api/accounts/99999/rules", headers=h,
                         json={"rules": {}}).status_code == 404

    def test_app_list_contains_wechat(self):
        """设备列表含微信小程序(官方 AVAILABLE_APPS 全量)。"""
        from app.services.qrcode import QrcodeLoginService
        apps = QrcodeLoginService._app_list()
        keys = {a["key"] for a in apps}
        assert "wechatmini" in keys       # 微信小程序
        assert "alipaymini" in keys       # 支付宝小程序
        assert "harmony" in keys          # 鸿蒙
        assert "tv" in keys               # 电视端
        assert len(apps) >= 18            # 官方全量 18 种

    def test_device_stored_as_chinese_label(self, monkeypatch):
        """info.device 存中文设备名(如 微信小程序)。"""
        from app.services.qrcode import QrcodeLoginService

        def fake_app_list():
            return [{"key": "wechatmini", "label": "115生活_微信小程序端"}]

        monkeypatch.setattr(QrcodeLoginService, "_app_list",
                            staticmethod(fake_app_list))
        assert QrcodeLoginService._app_label("wechatmini") == "115生活_微信小程序端"
        assert QrcodeLoginService._app_label("unknown") == "unknown"
