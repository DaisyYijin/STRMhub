"""STRM 任务执行服务: 三模式生成 + 防误删清理 + 统计。

设计来源: LitePan internal/strm/service.go + scanner.go:
- URL 三要素: {base}/api/redirect?key={b64url(file_id)}&t={token}
- 防误删: 只删 seen 之外; 远端扫描结果为空时中止清理(防误删保护)
- 三模式: missing(只补缺) / update(内容不同才重写) / full(全量重写)
"""
from __future__ import annotations

import base64
import json
import logging
from dataclasses import dataclass, field
from pathlib import Path

from ...drivers import registry
from ...drivers.base import FileItem
from . import scanner
from .writer import local_rel_path, safe_name, strm_file_name, write_strm

VALID_MODES = {"incremental_missing", "incremental_update", "full_sync"}

_STRM_SUFFIX = ".strm"

_log = logging.getLogger("strmhub.strm")


@dataclass
class TaskResult:
    generated: int = 0          # 实际生成(含命中)数量
    written: int = 0            # 真正发生写入
    skipped: int = 0            # 已存在/未变化跳过
    cleaned: int = 0            # 清理的失效 strm
    cleanup_aborted: bool = False  # 清理被阈值保护中止
    extra_downloaded: int = 0
    extra_failed: int = 0
    extra_skipped: int = 0
    meta_uploaded: int = 0      # 元数据回传网盘(LitePan 模式)
    meta_downloaded: int = 0    # 元数据从网盘补齐
    total_remote: int = 0
    error: str = ""
    records: list = field(default_factory=list)  # 新快照记录(path, file_key, size, mtime)


def build_strm_url(base_url: str, token: str, file_id: str) -> str:
    key = base64.urlsafe_b64encode(file_id.encode("utf-8")).rstrip(b"=").decode("ascii")
    return f"{base_url.rstrip('/')}/api/redirect?key={key}&t={token}"


class StrmService:
    """执行一个 STRM 任务(同步)。调用方负责事务与并发控制。"""

    DEFAULT_CLEANUP_MAX_RATIO = 0.5  # 待删 strm 占现存比例超过则中止清理

    def run(self, driver, remote_path: str, local_output: str, scan_mode: str,
            extensions: set[str], base_url: str, token: str,
            min_size: int = 0,
            snapshot: dict | None = None,
            cleanup_max_ratio: float = DEFAULT_CLEANUP_MAX_RATIO,
            extra: dict | None = None) -> TaskResult:
        """执行任务。

        snapshot: {rel_path: (size, mtime)} 上次快照; 非 full 模式下
                  (size, mtime) 未变化且本地 strm 已存在时跳过(增量 diff)。
        cleanup_max_ratio: 待删 .strm 占比超过该值(如 0.5)时中止清理。
        """
        if scan_mode not in VALID_MODES:
            raise ValueError(f"未知扫描模式: {scan_mode}")

        result = TaskResult()
        extra_exts = _extra_extensions(extra)
        try:
            candidates, extra_cands = scanner.collect_candidates(
                driver, remote_path, extensions, min_size, extra_exts)
        except Exception as exc:  # 远端不可达/超限
            result.error = str(exc)
            return result

        result.total_remote = len(candidates)
        out_root = Path(local_output)
        out_root.mkdir(parents=True, exist_ok=True)
        seen: set[Path] = set()
        use_snapshot = scan_mode != "full_sync" and snapshot is not None

        for cand in candidates:
            item = cand.item
            rel = local_rel_path(cand.rel_dirs)
            target = out_root / rel / strm_file_name(item.name)
            seen.add(target)
            result.generated += 1
            rel_path = "/".join(cand.rel_dirs + [item.name])

            # 增量 diff: 未变化且本地已存在 -> 跳过
            if use_snapshot:
                prev = snapshot.get(rel_path)
                if prev is not None and prev == (item.size, item.mtime or 0.0) \
                        and target.exists():
                    result.skipped += 1
                    result.records.append(
                        (rel_path, item.id, item.size, item.mtime or 0.0))
                    continue

            content = build_strm_url(base_url, token, item.id)
            try:
                if write_strm(target, content, _mode_writer(scan_mode)):
                    result.written += 1
                else:
                    result.skipped += 1
            except OSError as exc:
                result.error = f"写入失败 {target}: {exc}"
                break
            result.records.append(
                (rel_path, item.id, item.size, item.mtime or 0.0))

        # 伴生文件下载(AutoFilm 方案): 独立并发池, 同名匹配, 本地已有跳过
        if extra_exts and extra_cands:
            result.extra_skipped, result.extra_downloaded, result.extra_failed = (
                self._download_extras(driver, out_root, candidates,
                                      extra_cands, extra or {}))
            if result.extra_failed:
                _log.warning("[strm] %d 个伴生文件下载失败", result.extra_failed)

        # 元数据同步(LitePan 模式): local_primary/cloud_primary/bidirectional
        meta_mode = (extra or {}).get("metadata_sync") or "off"
        if meta_mode != "off":
            result.meta_uploaded, result.meta_downloaded = (
                self._metadata_sync(driver, out_root, candidates, meta_mode))

        # 防误删清理: 仅当远端扫描非空(避免远端异常导致全库误删)
        if candidates:
            result.cleaned, aborted = self._cleanup_stale(
                out_root, seen, cleanup_max_ratio)
            result.cleanup_aborted = aborted
            if aborted:
                result.error = (result.error + "; " if result.error else "") + \
                    "清理中止: 待删 .strm 比例超过安全阈值, 请人工确认"

        return result

    def _download_extras(self, driver, out_root: Path,
                         candidates: list[scanner.Candidate],
                         extras: list[scanner.Candidate],
                         extra_cfg: dict) -> tuple[int, int, int]:
        """伴生文件下载: 与视频同目录同名(stem 相同)才下载。

        独立线程池并发(extra.concurrency, 默认 4), 本地已存在跳过
        (过期重下由删除本地文件触发); 失败不阻塞主流程。
        返回 (skipped, downloaded, failed)。
        """
        concurrency = max(1, int(extra_cfg.get("concurrency") or 4))
        video_map: set[tuple[str, str]] = set()
        for c in candidates:
            video_map.add((_stem_of(c.item.name), "/".join(c.rel_dirs)))
        tasks = []
        for ex in extras:
            key = (_stem_of(ex.item.name), "/".join(ex.rel_dirs))
            if key not in video_map:
                continue
            target = out_root.joinpath(*ex.rel_dirs, ex.item.name)
            if target.exists():
                continue
            tasks.append((ex, target))
        skipped = len(extras) - len(tasks)
        if not tasks:
            return skipped, 0, 0

        def _dl(ex: scanner.Candidate, target: Path) -> bool:
            try:
                url, _headers = driver.resolve_download(ex.item)
                if not url:
                    return False
                target.parent.mkdir(parents=True, exist_ok=True)
                return _download_http(url, target)
            except Exception:
                return False

        if concurrency == 1:
            done = failed = 0
            for ex, target in tasks:
                if _dl(ex, target):
                    done += 1
                else:
                    failed += 1
        else:
            from concurrent.futures import ThreadPoolExecutor
            with ThreadPoolExecutor(max_workers=concurrency) as pool:
                results = list(pool.map(lambda t: _dl(t[0], t[1]), tasks))
            done = sum(1 for r in results if r)
            failed = len(results) - done
        return skipped, done, failed

    _META_EXTS = {".nfo", ".jpg", ".jpeg", ".png", ".webp",
                     ".srt", ".ass", ".vtt", ".ssa"}

    def _metadata_sync(self, driver, out_root: Path,
                       candidates: list[scanner.Candidate],
                       mode: str) -> tuple[int, int]:
        """元数据回传/补齐(LitePan 模式)。

        - local_primary/bidirectional: 双向补缺(本地刮削/下载的 nfo 图片上传
          网盘同目录, 网盘有而本地无的下载回来; 都不覆盖已有)
        - cloud_primary: 仅网盘 -> 本地补齐(不上传, 云端为权威)
        只处理与视频同目录的元数据(同名 stem 匹配); 作品根 tvshow.nfo 暂不涉及。
        """
        do_upload = mode in ("local_primary", "bidirectional")
        do_download = mode in ("local_primary", "cloud_primary", "bidirectional")
        if not hasattr(driver, "upload"):
            _log.warning("[strm] 驱动不支持上传, 元数据同步仅执行下载方向")
            do_upload = False
        # 目录去重: parent_id -> (rel_dirs, 视频 stems)
        dirs: dict[str, tuple[list[str], set[str]]] = {}
        for c in candidates:
            entry = dirs.setdefault(c.parent_id or "0", (c.rel_dirs, set()))
            entry[1].add(_stem_of(c.item.name))
        uploaded = downloaded = 0
        for parent_id, (rel_dirs, stems) in dirs.items():
            local_dir = out_root.joinpath(*rel_dirs)
            try:
                remote_rows = driver.list_files(parent_id)
            except Exception:
                continue
            remote_by_name = {r.name.lower(): r for r in remote_rows}
            # 下载补齐(网盘有、本地无)
            if do_download:
                for r in remote_rows:
                    if r.is_dir or not r.name.lower().endswith(tuple(self._META_EXTS)):
                        continue
                    if _stem_of(r.name) not in stems:
                        continue
                    target = local_dir / r.name
                    if target.exists():
                        continue
                    try:
                        url, _h = driver.resolve_download(r)
                        if url and _download_http(url, target):
                            downloaded += 1
                    except Exception:
                        continue
            # 上传补齐(本地有、网盘无)
            if do_upload and local_dir.exists():
                for lp in local_dir.iterdir():
                    if not lp.is_file() or lp.suffix.lower() not in self._META_EXTS:
                        continue
                    if _stem_of(lp.name) not in stems:
                        continue
                    if lp.name.lower() in remote_by_name:
                        continue  # 网盘已有 -> 不覆盖(LitePan 补缺语义)
                    try:
                        if driver.upload(str(lp), parent_id, lp.name):
                            uploaded += 1
                    except Exception:
                        continue
        return uploaded, downloaded

    def _cleanup_stale(self, out_root: Path, seen: set[Path],
                       max_ratio: float) -> tuple[int, bool]:
        """删除 out_root 下不在 seen 中的 .strm(连带空目录)。

        返回 (cleaned, aborted); 待删比例 > max_ratio 时中止(防误删)。
        """
        cleaned = 0
        if not out_root.exists():
            return 0, False
        stale = [p for p in sorted(out_root.rglob(f"*{_STRM_SUFFIX}")) if p not in seen]
        total = len(stale) + len(seen)
        if total and len(stale) / total > max_ratio:
            return 0, True
        for p in stale:
            try:
                p.unlink()
                cleaned += 1
            except OSError:
                pass
        # 清理空目录(自底向上)
        for d in sorted([p for p in out_root.rglob("*") if p.is_dir()],
                        key=lambda p: -len(p.parts)):
            try:
                d.rmdir()
            except OSError:
                pass
        return cleaned, False


def _stem_of(name: str) -> str:
    return Path(name).stem.lower()


def _extra_extensions(extra: dict | None) -> set[str]:
    """由 extra 配置构建伴生扩展名白名单(AutoFilm 方案)。"""
    if not extra:
        return set()
    exts: set[str] = set()
    if extra.get("subtitle"):
        exts |= {".srt", ".ass", ".vtt", ".ssa"}
    if extra.get("image"):
        exts |= {".jpg", ".jpeg", ".png", ".webp"}
    if extra.get("nfo"):
        exts |= {".nfo"}
    for e in (extra.get("other_ext") or []):
        e = str(e).lower()
        exts.add(e if e.startswith(".") else f".{e}")
    return exts


def _download_http(url: str, dest: Path) -> bool:
    """下载 URL 到文件(可被测试替换)。"""
    import httpx
    try:
        with httpx.Client(timeout=60) as client:
            with client.stream("GET", url) as resp:
                if resp.status_code != 200:
                    return False
                tmp = dest.with_suffix(dest.suffix + ".part")
                with open(tmp, "wb") as f:
                    for chunk in resp.iter_bytes(chunk_size=65536):
                        f.write(chunk)
                tmp.replace(dest)
        return True
    except Exception:
        try:
            dest.with_suffix(dest.suffix + ".part").unlink(missing_ok=True)
        except OSError:
            pass
        return False


def _mode_writer(scan_mode: str) -> str:
    return {"incremental_missing": "missing",
            "incremental_update": "update",
            "full_sync": "full"}[scan_mode]


def task_to_extensions(extensions_json: str) -> set[str]:
    if not extensions_json:
        return set(scanner.DEFAULT_EXTENSIONS)
    return {e.lower() if e.startswith(".") else f".{e.lower()}"
            for e in json.loads(extensions_json)}
