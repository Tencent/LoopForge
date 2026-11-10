HOST = "dsh"


def describe() -> str:
    return "DeepSeek Harness project workflow target"


def materialize(edition, stage) -> None:
    from devflow_cli.core import materialize_portable
    if edition != "portable":
        raise ValueError("DeepSeek Harness 只支持 Portable edition")
    materialize_portable(HOST, stage)
