"""123 云盘: 扫码流程(p123client 契约) + 驱动 API 调用测试。"""
from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from app.main import app


class FakeP123Client:
    """模拟 p123client 关键方法(按签名契约)。"""

    def __init__(self, token=None, passport="", password=""):
        self.token = token

    def fs_list(self, payload: dict, **kw):
        assert isinstance(payload, dict), "fs_list 必须传 payload dict"
        assert payload["parentFileId"] == 0
        if payload.get("lastFileId"):
            return {"code": 0, "data": {"fileList": [], "lastFileId": -1}}
        return {"code": 0, "data": {"fileList": [
            {"FileID": 101, "FileName": "电影", "Type": 0, "Size": 0},          # 目录
            {"FileID": 102, "FileName": "a.mkv", "Type": 2, "Size": 1024},      # 视频
        ], "lastFileId": -1}}

    def fs_mkdir(self, payload: dict, **kw):
        return {"code": 0, "data": {"FileID": 200}}

    def fs_move(self, payload: dict, **kw):
        assert payload["fileIDs"] == [102]
        assert payload["toParentFileID"] == 200
        return {"code": 0, "data": {}}

    def fs_rename(self, payload: dict, **kw):
        assert payload["renameList"] == ["102|新名字.mkv"]
        return {"code": 0, "data": {}}

    def download_info(self, payload, **kw):
        assert payload == 102
        return {"code": 0, "data": {"DownloadUrl": "https://dl/102"}}

    def user_info(self, **kw):
        return {"code": 0, "data": {"userName": "tester"}}


@pytest.fixture(scope="module")
def client():
    with TestClient(app) as c:
        yield c


def _login(c):
    return c.post("/api/auth/login",
                  json={"password": "testpass"}).json()["token"]


def test_driver_calls(monkeypatch):
    """驱动按 p123client 契约调用(全部 payload dict)。"""
    import app.drivers.p123.driver as mod
    fake = FakeP123Client()
    d = mod.P123Driver(credential="token-x", client=fake)
    items = d.list_files("0")
    assert [i.name for i in items] == ["电影", "a.mkv"]
    assert items[0].is_dir and not items[1].is_dir
    url, _ = d.resolve_download(items[1])
    assert url == "https://dl/102"
    fid = d.create_folder("0", "新建")
    assert fid == "200"
    moved = d.move(items[1], "200", "新名字.mkv")
    assert moved.name == "新名字.mkv"
    assert d.ping()


def test_p123_scan_start_and_poll(monkeypatch, client):
    """扫码 start(uni_id + 二维码)与 poll 轮询/确认流程。"""
    import app.services.qrcode as svc
    calls = {"poll": 0}

    class Fake123QR:
        @staticmethod
        def login_qrcode_generate(**kw):
            return {"code": 0, "data": {"uniID": "u123",
                                        "qrCode": "https://qr/123"}}

        @staticmethod
        def login_qrcode_result(payload, **kw):
            calls["poll"] += 1
            assert payload == {"uniID": "u123"}
            if calls["poll"] < 2:
                return {"code": 0, "data": {"loginStatus": 0}}
            return {"code": 0, "data": {"loginStatus": 3,
                                        "token": "tok-123"}}

    # 直接用真实 start(依赖 p123client): 打桩 generate
    monkeypatch.setattr("p123client.P123Client.login_qrcode_generate",
                        staticmethod(Fake123QR.login_qrcode_generate))
    monkeypatch.setattr("p123client.P123Client.login_qrcode_result",
                        staticmethod(Fake123QR.login_qrcode_result))

    h = {"Authorization": f"Bearer {_login(client)}"}
    r = client.post("/api/accounts/qrcode/start", headers=h,
                    json={"driver_type": "p123"})
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["uni_id"] == "u123"
    assert body["qr_image"].startswith("data:image/svg+xml")

    r = client.post("/api/accounts/qrcode/poll", headers=h,
                    json={"driver_type": "p123", "uni_id": "u123"})
    assert r.status_code == 200, r.text
    assert r.json()["status"] == "waiting"

    r = client.post("/api/accounts/qrcode/poll", headers=h,
                    json={"driver_type": "p123", "uni_id": "u123"})
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["status"] == "confirmed"
    assert body["account"]["driver_type"] == "p123"
    # 账户已持久化(列表接口不返回凭据, 属安全设计)
    got = client.get("/api/accounts", headers=h).json()
    acc = [a for a in got if a["id"] == body["account"]["id"]][0]
    assert acc["status"] == "ok"
