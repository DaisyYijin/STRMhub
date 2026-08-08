"""伴生文件下载测试(AutoFilm 方案): 同名匹配/独立并发/跳过已存在。"""
from __future__ import annotations

import json

import pytest

from app.services.strm.scanner import collect_candidates
from app.services.strm.service import StrmService, _extra_extensions


class FakeDriver:
    """本地文件驱动: 目录树 = 文件系统, 便于断言伴生下载。"""

    def __init__(self, root):
        self.root = root
        self.downloaded: list[str] = []

    def list_files(self, parent_id: str, only_dirs: bool = False):
        from app.drivers.base import FileItem
        base = self.root if parent_id in ("", "0") else self.root / parent_id
        items = []
        for p in sorted(base.iterdir()):
            items.append(FileItem(
                id=p.name, name=p.name, size=p.stat().st_size,
                is_dir=p.is_dir()))
        return items

    def resolve_download(self, item):
        # 直链 = file:// 内容直接返回(测试不真下载, 由 monkeypatch 处理)
        return f"http://fake/{item.id}", {}


def test_extra_extensions_mapping():
    """extra 配置 -> 扩展名白名单(内置三类 + 自定义)。"""
    exts = _extra_extensions({
        "subtitle": True, "image": True, "nfo": True,
        "other_ext": ["zip", ".md"], "concurrency": 5,
    })
    assert ".srt" in exts and ".ass" in exts
    assert ".jpg" in exts and ".png" in exts
    assert ".nfo" in exts
    assert ".zip" in exts and ".md" in exts  # 无点自动补


def test_scanner_collects_extras(tmp_path):
    """扫描: 视频候选 + 伴生文件候选(同名不同扩展名)。"""
    from app.drivers.base import FileItem

    (tmp_path / "电影.mkv").write_bytes(b"v")
    (tmp_path / "电影.srt").write_bytes(b"s")
    (tmp_path / "电影.nfo").write_bytes(b"n")
    (tmp_path / "另一个.mkv").write_bytes(b"v2")
    (tmp_path / "孤儿.srt").write_bytes(b"x")  # 无同名视频 -> 不算伴生

    cands, extras = collect_candidates(
        FakeDriver(tmp_path), "0", {".mkv"}, 0, _extra_extensions(
            {"subtitle": True, "nfo": True}))
    assert [c.item.name for c in cands] == ["另一个.mkv", "电影.mkv"]
    # 扫描收集全部 extra 文件(同名过滤在 run 阶段, 孤儿.srt 也会被收集)
    assert {e.item.name for e in extras} == {"电影.nfo", "电影.srt", "孤儿.srt"}


def test_run_downloads_extras(tmp_path, monkeypatch):
    """run: 同名伴生下载到本地, 已存在跳过, 失败计数。"""
    (tmp_path / "电影.mkv").write_bytes(b"v")
    (tmp_path / "电影.srt").write_bytes(b"s")
    out = tmp_path / "out"
    downloaded: list[str] = []

    def fake_download(url, dest):
        downloaded.append(dest.name)
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(b"DL")
        return True

    monkeypatch.setattr("app.services.strm.service._download_http",
                        fake_download)
    svc = StrmService()
    result = svc.run(
        FakeDriver(tmp_path), "0", str(out), "incremental_missing",
        {".mkv"}, "http://hub:6060", "tok1",
        extra={"subtitle": True, "concurrency": 4})
    print("DBG result:", result)
    print("DBG out files:", sorted(str(x.relative_to(out)) for x in out.rglob('*')))
    assert result.generated == 1
    assert (out / "电影.strm").exists()
    assert result.extra_downloaded == 1
    assert (out / "电影.srt").read_bytes() == b"DL"
    # 二次运行: 已存在 -> 跳过
    result2 = svc.run(
        FakeDriver(tmp_path), "0", str(out), "incremental_missing",
        {".mkv"}, "http://hub:6060", "tok1",
        extra={"subtitle": True, "concurrency": 4})
    assert result2.extra_downloaded == 0
    assert result2.extra_skipped >= 1


def test_run_extra_failure_tolerated(tmp_path, monkeypatch):
    """下载失败不影响 strm 生成主流程。"""
    (tmp_path / "电影.mkv").write_bytes(b"v")
    (tmp_path / "电影.srt").write_bytes(b"s")
    out = tmp_path / "out"

    def fail_download(url, dest):
        return False

    monkeypatch.setattr("app.services.strm.service._download_http",
                        fail_download)
    result = StrmService().run(
        FakeDriver(tmp_path), "0", str(out), "incremental_missing",
        {".mkv"}, "http://hub:6060", "tok1",
        extra={"subtitle": True, "concurrency": 1})
    assert result.generated == 1  # strm 正常
    assert result.extra_failed == 1
    assert not result.error  # 不阻塞主流程


class MetaSyncDriver(FakeDriver):
    """带上传能力的驱动: 记录上传; 网盘侧可预置元数据文件。"""

    def __init__(self, root, remote_nfo=None):
        super().__init__(root)
        self.uploaded: list[str] = []
        self.remote_nfo = remote_nfo  # 网盘已有元数据名(模拟远程存在)

    def upload(self, local_path, parent_id, name):
        self.uploaded.append(name)
        return True


def test_metadata_sync_local_primary(tmp_path, monkeypatch):
    """local_primary: 本地 nfo 上传(网盘无) + 网盘字幕下载(本地无), 均不覆盖已有。"""
    from app.services.strm.service import StrmService
    (tmp_path / "电影.mkv").write_bytes(b"v")
    (tmp_path / "电影.srt").write_bytes(b"S")   # 网盘已有字幕 -> 下载补齐
    out = tmp_path / "out"
    out.mkdir()
    (out / "电影.nfo").write_bytes(b"N")         # 本地刮削产物 -> 上传补齐

    driver = MetaSyncDriver(tmp_path)

    def fake_download(url, dest):
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(b"SRT")
        return True

    monkeypatch.setattr("app.services.strm.service._download_http",
                        fake_download)
    svc = StrmService()
    result = svc.run(
        driver, "0", str(out), "incremental_missing",
        {".mkv"}, "http://hub:6060", "tok1",
        extra={"metadata_sync": "local_primary", "concurrency": 4})
    # 上传: 本地 nfo 网盘没有 -> 上传
    assert result.meta_uploaded == 1
    assert "电影.nfo" in driver.uploaded
    # 下载: 网盘字幕本地没有 -> 下载
    assert result.meta_downloaded == 1
    assert (out / "电影.srt").read_bytes() == b"SRT"


def test_metadata_sync_cloud_primary_no_upload(tmp_path, monkeypatch):
    """cloud_primary: 只下载补齐, 不上传。"""
    from app.services.strm.service import StrmService
    (tmp_path / "电影.mkv").write_bytes(b"v")
    (tmp_path / "电影.nfo").write_bytes(b"N")
    out = tmp_path / "out"
    out.mkdir()

    driver = MetaSyncDriver(tmp_path)

    def fake_download(url, dest):
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(b"X")
        return True

    monkeypatch.setattr("app.services.strm.service._download_http",
                        fake_download)
    result = StrmService().run(
        driver, "0", str(out), "incremental_missing",
        {".mkv"}, "http://hub:6060", "tok1",
        extra={"metadata_sync": "cloud_primary"})
    assert result.meta_uploaded == 0
    assert driver.uploaded == []


def test_metadata_sync_driver_without_upload(tmp_path, monkeypatch):
    """驱动无上传能力(如 123): 自动降级为仅下载, 不报错。"""
    from app.services.strm.service import StrmService
    (tmp_path / "电影.mkv").write_bytes(b"v")
    out = tmp_path / "out"
    out.mkdir()
    driver = FakeDriver(tmp_path)  # 无 upload
    result = StrmService().run(
        driver, "0", str(out), "incremental_missing",
        {".mkv"}, "http://hub:6060", "tok1",
        extra={"metadata_sync": "bidirectional"})
    assert result.generated == 1
    assert result.meta_uploaded == 0
