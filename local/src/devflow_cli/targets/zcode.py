HOST = "zcode"


def describe() -> str:
    return "ZCode project workflow target"


def materialize(edition, stage) -> None:
    from devflow_cli.core import materialize_classic, materialize_portable
    (materialize_portable if edition == "portable" else materialize_classic)(HOST, stage)
