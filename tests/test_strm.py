"""STRM 生成核心测试: 闭环/三模式/扩展名过滤/SafeName/防误删。"""
from __future__ import annotations

from pathlib import Path

from app.drivers.registry import create
from app.services.strm.service import StrmService, build_strm_url
from app.services.strm.writer import safe_name, strm_file_name


def _make_source(tmp_path: Path) -> Path:
    """构造媒体源目录。注意: Windows 文件系统不允许非法字符, 故只用合法名。"""
    src = tmp_path / "media"
    (src / "Movie (2024)").mkdir(parents=True)
    (src / "Movie (2024)" / "movie.mkv").write_bytes(b"m")
    (src / "Movie (2024)" / "poster.jpg").write_bytes(b"p")   # 应被跳过
    (src / "TV Show").mkdir()
    (src / "TV Show" / "Season 1").mkdir()
    (src / "TV Show" / "Season 1" / "S01E01 1080p.mp4").write_bytes(b"v")
    (src / "TV Show" / "Season 1" / "S01E01.ass").write_bytes(b"s")  # 应被跳过
    (src / "Anime 01.mkv").write_bytes(b"b")
    return src


class TestStrmService:
    def _run(self, src: Path, out: Path, mode="incremental_missing"):
        driver = create("local")
        return StrmService().run(
            driver=driver,
            remote_path=str(src),
            local_output=str(out),
            scan_mode=mode,
            extensions=set(),
            base_url="http://hub:6060",
            token="t123",
        )

    def test_full_flow(self, tmp_path: Path):
        src = _make_source(tmp_path)
        out = tmp_path / "strm"
        result = self._run(src, out)

        assert result.error == ""
        assert result.total_remote == 3  # mkv/mp4/mkv
        assert result.written == 3
        assert result.generated == 3

        # 目录结构: 路径映射 + .strm 命名
        movie_strm = out / "Movie (2024)" / "movie.strm"
        assert movie_strm.exists()
        tv_strm = out / "TV Show" / "Season 1" / "S01E01 1080p.strm"
        assert tv_strm.exists()
        anime_strm = out / "Anime 01.strm"
        assert anime_strm.exists()

        # 内容: URL 三要素
        content = movie_strm.read_text(encoding="utf-8")
        assert content.startswith("http://hub:6060/api/redirect?key=")
        assert "&t=t123" in content

        # 元数据旁路文件不生成 strm
        assert not (out / "Movie (2024)" / "poster.strm").exists()
        assert not (out / "TV Show" / "Season 1" / "S01E01.ass.strm").exists()

    def test_modes(self, tmp_path: Path):
        src = _make_source(tmp_path)
        out = tmp_path / "strm"
        self._run(src, out, "incremental_missing")
        movie_strm = out / "Movie (2024)" / "movie.strm"

        # missing 模式: 已存在则跳过
        r2 = self._run(src, out, "incremental_missing")
        assert r2.written == 0 and r2.skipped == 3

        # update 模式: 内容相同也跳过
        r3 = self._run(src, out, "incremental_update")
        assert r3.written == 0 and r3.skipped == 3

        # full 模式: 总是重写
        r4 = self._run(src, out, "full_sync")
        assert r4.written == 3 and r4.skipped == 0

    def test_base_url_change_rewrites_in_update_mode(self, tmp_path: Path):
        src = _make_source(tmp_path)
        out = tmp_path / "strm"
        self._run(src, out)

        # 服务器地址变化: update 模式应重写内容
        driver = create("local")
        result = StrmService().run(
            driver=driver, remote_path=str(src), local_output=str(out),
            scan_mode="incremental_update", extensions=set(),
            base_url="http://new-host:9000", token="t123")
        assert result.written == 3
        content = (out / "Movie (2024)" / "movie.strm").read_text(encoding="utf-8")
        assert content.startswith("http://new-host:9000/")

    def test_stale_cleanup(self, tmp_path: Path):
        src = _make_source(tmp_path)
        out = tmp_path / "strm"
        self._run(src, out)
        stale = out / "Movie (2024)" / "movie.strm"
        assert stale.exists()

        # 远端删除 movie.mkv 后重跑 -> 对应 strm 被清理
        (src / "Movie (2024)" / "movie.mkv").unlink()
        r = self._run(src, out)
        assert r.cleaned == 1
        assert not stale.exists()

    def test_cleanup_aborted_when_remote_empty(self, tmp_path: Path):
        """防误删保护: 远端扫描结果为空(异常/空源)时禁止清理。"""
        src = tmp_path / "media"
        src.mkdir()
        (src / "keep.mkv").write_bytes(b"k")
        out = tmp_path / "strm"
        self._run(src, out)
        assert (out / "keep.strm").exists()

        # 远端文件全消失(模拟目录被清空): 不应清理本地
        for f in src.iterdir():
            f.unlink()
        r = self._run(src, out)
        assert r.cleaned == 0
        assert (out / "keep.strm").exists()

    def test_cleanup_ratio_aborts(self, tmp_path: Path):
        """清理阈值保护: 待删比例超过 0.5 时中止清理。"""
        src = _make_source(tmp_path)
        out = tmp_path / "strm"
        self._run(src, out)
        # 3 个垃圾 strm: 待删 3, 现存 6, 比例 0.5 -> 不超阈值, 正常清理
        for i in range(3):
            (out / f"junk{i}.strm").write_text("junk", encoding="utf-8")
        r = self._run(src, out)
        assert r.cleaned == 3 and not r.cleanup_aborted

        # 再一次性造 6 个垃圾: 待删 6, 现存 9, 比例 0.67 > 0.5 -> 中止
        for i in range(6):
            (out / f"junk{i}.strm").write_text("junk", encoding="utf-8")
        r = self._run(src, out)
        assert r.cleaned == 0 and r.cleanup_aborted
        assert "安全阈值" in r.error

    def _run_with_snapshot(self, src: Path, out: Path, snapshot=None,
                           mode="incremental_update",
                           base_url="http://hub:6060"):
        driver = create("local")
        result = StrmService().run(
            driver=driver, remote_path=str(src), local_output=str(out),
            scan_mode=mode, extensions=set(),
            base_url=base_url, token="t123",
            snapshot=snapshot)
        return result

    def test_incremental_snapshot_skips_unchanged(self, tmp_path: Path):
        """增量 diff: 快照中未变化的文件第二次运行完全跳过。"""
        src = _make_source(tmp_path)
        out = tmp_path / "strm"
        r1 = self._run_with_snapshot(src, out, snapshot=None)
        assert r1.written == 3

        snapshot = {p: (s, m) for p, _k, s, m in r1.records}
        r2 = self._run_with_snapshot(src, out, snapshot=snapshot)
        assert r2.written == 0 and r2.skipped == 3
        assert r2.records == r1.records

    def test_snapshot_picks_up_changed_file(self, tmp_path: Path):
        """增量 diff: 文件变化(size 变)时被重新处理, 未变化文件被快照跳过。

        配合 base_url 变化制造内容差异: 变化文件重写(written=1),
        未变化文件走快照跳过(skipped=2) —— 证明 diff 只处理变化部分。
        """
        src = _make_source(tmp_path)
        out = tmp_path / "strm"
        r1 = self._run_with_snapshot(src, out, snapshot=None)
        snapshot = {p: (s, m) for p, _k, s, m in r1.records}

        # 修改一个文件(内容变长 -> size 变化) + base_url 变化(内容不同)
        target = src / "Movie (2024)" / "movie.mkv"
        target.write_bytes(b"m" * 100)
        assert target.stat().st_size == 100, "sanity: 文件应已修改"
        r2 = self._run_with_snapshot(src, out, snapshot=snapshot,
                                     base_url="http://hub:9000")
        assert r2.written == 1 and r2.skipped == 2
        content = (out / "Movie (2024)" / "movie.strm").read_text(encoding="utf-8")
        assert content.startswith("http://hub:9000/")
        # 快照已更新: movie.mkv 的 size 记录为 100
        snap2 = {p: (s, m) for p, _k, s, m in r2.records}
        assert snap2["Movie (2024)/movie.mkv"][0] == 100

    def test_snapshot_repairs_missing_strm(self, tmp_path: Path):
        """增量 diff: 快照未变但本地 strm 被删 -> 补写(补缺)。"""
        src = _make_source(tmp_path)
        out = tmp_path / "strm"
        r1 = self._run_with_snapshot(src, out, snapshot=None)
        snapshot = {p: (s, m) for p, _k, s, m in r1.records}

        (out / "Movie (2024)" / "movie.strm").unlink()
        r2 = self._run_with_snapshot(src, out, snapshot=snapshot)
        assert r2.written == 1 and r2.skipped == 2
        assert (out / "Movie (2024)" / "movie.strm").exists()


class TestWriterHelpers:
    def test_safe_name(self):
        assert safe_name("a/b\\c:d*e?f\"g<h>i|j") == "a_b_c_d_e_f_g_h_i_j"
        assert safe_name("") == "_"
        assert safe_name(".") == "_"
        assert safe_name("..") == "_"
        assert safe_name("normal 名 字.mp4") == "normal 名 字.mp4"

    def test_strm_file_name(self):
        assert strm_file_name("movie.mkv") == "movie.strm"
        assert strm_file_name("movie.iso") == "movie.iso.strm"
        assert strm_file_name("a:b.mkv") == "a_b.strm"

    def test_build_strm_url(self):
        url = build_strm_url("http://h:8000/", "tok", "C:/path/x.mkv")
        assert url.startswith("http://h:8000/api/redirect?key=")
        assert "&t=tok" in url
        # 可逆: key 是 base64url 的路径
        import base64
        key = url.split("key=")[1].split("&")[0]
        padded = key + "=" * (-len(key) % 4)
        assert base64.urlsafe_b64decode(padded).decode() == "C:/path/x.mkv"
