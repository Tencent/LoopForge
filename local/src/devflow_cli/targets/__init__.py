from importlib import import_module


def load(host: str):
    return import_module(f"devflow_cli.targets.{host}")
