"""路径映射与写入。

设计来源: LitePan internal/strm/writer.go —— SafeName 替换非法字符防路径穿越,
逐级拼装本地相对路径; 超长路径组件收集失败而非中断。
"""
from __future__ import annotations

from pathlib import Path

# Windows/Unix 均非法的字符 -> 下划线(参考 LitePan SafeName)
_ILLEGAL = set('/\\:*?"<>|')
# 保留设备名(Windows)不处理, 统一走替换即可


def safe_name(name: str) -> str:
    """把非法字符替换为 _, 空名/./.. 兜底 _。"""
    out = "".join("_" if ch in _ILLEGAL else ch for ch in name)
    out = out.strip()
    if not out or out in (".", ".."):
        return "_"
    return out


def local_rel_path(rel_dirs: list[str]) -> Path:
    """网盘子目录链 -> 本地相对路径(逐级 SafeName)。"""
    p = Path()
    for d in rel_dirs:
        p = p / safe_name(d)
    return p


def strm_file_name(media_name: str, iso_compat: bool = True) -> str:
    """媒体主名 -> .strm 文件名(去扩展名; ISO 用 .iso.strm 便于 Infuse 识别)。

    注意: 不用 Path().stem —— Windows 下 "a:b.mkv" 会被误解析为盘符 a:。
    """
    base = media_name.rsplit(".", 1)[0] if "." in media_name else media_name
    if iso_compat and media_name.lower().endswith(".iso"):
        return f"{safe_name(base)}.iso.strm"
    return f"{safe_name(base)}.strm"


def write_strm(path: Path, content: str, mode: str) -> bool:
    """写入 .strm, 返回是否发生了写入(供增量模式判断)。

    mode: full 总是写; update 内容不同才写; missing 已存在则跳过。
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    if mode == "missing" and path.exists():
        return False
    if mode == "update" and path.exists() and path.read_text(encoding="utf-8") == content:
        return False
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(content, encoding="utf-8")
    tmp.replace(path)  # 原子替换
    return True
