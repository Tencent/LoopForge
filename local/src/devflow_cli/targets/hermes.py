HOST = "hermes"


def describe() -> str:
    return "Hermes Agent project workflow target"


def materialize(edition, stage) -> None:
    from devflow_cli.core import materialize_portable
    if edition != "portable":
        raise ValueError("Hermes Agent 只支持 Portable edition")
    materialize_portable(HOST, stage)
