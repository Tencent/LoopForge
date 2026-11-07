"""Tencent Cloud regions shown in the Initializr picker."""

from __future__ import annotations

REGIONS: tuple[tuple[str, str], ...] = (
    ("ap-guangzhou", "广州"),
    ("ap-shanghai", "上海"),
    ("ap-nanjing", "南京"),
    ("ap-beijing", "北京"),
    ("ap-chengdu", "成都"),
    ("ap-chongqing", "重庆"),
    ("ap-hongkong", "中国香港"),
    ("ap-singapore", "新加坡"),
    ("ap-tokyo", "东京"),
    ("ap-seoul", "首尔"),
    ("ap-bangkok", "曼谷"),
    ("ap-jakarta", "雅加达"),
    ("ap-mumbai", "孟买"),
    ("na-ashburn", "弗吉尼亚"),
    ("na-siliconvalley", "硅谷"),
    ("eu-frankfurt", "法兰克福"),
    ("sa-saopaulo", "圣保罗"),
)


def public_regions() -> list[dict[str, str]]:
    return [{"id": rid, "name": name} for rid, name in REGIONS]
