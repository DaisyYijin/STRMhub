"""目录树扫描: 遍历驱动目录, 收集媒体候选(带相对路径链)。

设计来源: LitePan internal/strm/scanner.go walkScope。
"""
from __future__ import annotations

from dataclasses import dataclass

from ...drivers.base import FileItem

# 默认媒体扩展名(与竞品对齐: 视频+音频)
DEFAULT_EXTENSIONS = {
    ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".ts", ".m2ts",
    ".iso", ".mp3", ".flac", ".wav", ".m4a", ".aac", ".ogg", ".opus",
}

# 元数据/刮削旁路文件(不生成 STRM)
SKIP_EXTENSIONS = {".nfo", ".jpg", ".jpeg", ".png", ".webp", ".srt", ".ass", ".txt", ".json"}

MAX_DEPTH = 40
MAX_FILES = 3000  # 单任务上限, 防止失控(参考 LitePan maxScanFiles)


@dataclass
class Candidate:
    item: FileItem
    rel_dirs: list[str]  # 相对 remote_path 的目录链(不含自身)


def should_generate(name: str, extensions: set[str], min_size: int = 0) -> bool:
    """扩展名/大小过滤。"""
    lower = name.lower()
    if not extensions or lower.endswith(tuple(extensions)):
        return True
    return False


def collect_candidates(driver, root_id: str, extensions: set[str],
                       min_size: int = 0,
                       extra_exts: set[str] | None = None):
    """深度优先遍历(每目录一次 API), 返回 (媒体候选, 伴生文件候选)。

    extra_exts: 伴生文件扩展名白名单(如 .srt/.nfo/.jpg), 命中且与视频
    同名的文件作为伴生候选收集(不生成 STRM, 由调用方下载)。
    超过 MAX_FILES 直接抛异常(避免失控); 目录超深跳过。
    """
    out: list[Candidate] = []
    extras: list[Candidate] = []
    _walk(driver, root_id, [], extensions, min_size, out, extras, extra_exts or set())
    return out, extras


def _walk(driver, parent_id: str, dirs: list[str], extensions: set[str],
          min_size: int, out: list[Candidate], extras: list[Candidate],
          extra_exts: set[str], depth: int = 0) -> None:
    if depth > MAX_DEPTH:
        return
    for item in driver.list_files(parent_id):
        if item.is_dir:
            _walk(driver, item.id, dirs + [item.name], extensions, min_size,
                  out, extras, extra_exts, depth + 1)
        else:
            lower = item.name.lower()
            if lower.endswith(tuple(SKIP_EXTENSIONS)):
                if extra_exts and lower.endswith(tuple(extra_exts)):
                    # 伴生文件候选(下载用, 不生成 STRM)
                    extras.append(Candidate(item=item, rel_dirs=list(dirs)))
                continue
            if extensions and not lower.endswith(tuple(extensions)):
                continue
            if min_size and item.size < min_size:
                continue
            out.append(Candidate(item=item, rel_dirs=list(dirs)))
            if len(out) > MAX_FILES:
                raise RuntimeError(f"文件数超过上限 {MAX_FILES}, 请收窄扫描范围")
