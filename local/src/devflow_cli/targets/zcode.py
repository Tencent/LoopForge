HOST = "zcode"


def describe() -> str:
    return "ZCode project workflow target"


def materialize(edition, stage) -> None:
    from devflow_cli.core import materialize_portable
    if edition != "portable":
        raise ValueError("ZCode 只支持 Portable edition")
    materialize_portable(HOST, stage)
