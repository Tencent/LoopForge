HOST = "opencode"


def describe() -> str:
    return "OpenCode project workflow target"


def materialize(edition, stage) -> None:
    from devflow_cli.core import materialize_classic, materialize_portable
    (materialize_portable if edition == "portable" else materialize_classic)(HOST, stage)
