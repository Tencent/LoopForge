"""Shared request/response models for the Initializr API."""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, Field


class ProjectSpec(BaseModel):
    name: str = "demo"
    description: str = "Tencent Cloud ops MCP"
    target: str = "prod"
    transport: Literal["stdio", "http"] = "stdio"
    region: str = "ap-guangzhou"


class ResourceItem(BaseModel):
    id: str
    alias: str
    name: str = ""
    region: str = ""
    extra: dict[str, Any] = Field(default_factory=dict)


class CLSTopic(BaseModel):
    id: str
    alias: str
    name: str = ""


class CLSResource(BaseModel):
    logset_id: str = Field(alias="logsetId")
    logset_name: str = Field(default="", alias="logsetName")
    region: str = ""
    topics: list[CLSTopic] = Field(default_factory=list)

    model_config = {"populate_by_name": True}


class TDSQLCResource(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    instance_id: str = Field(alias="instanceId")
    alias: str
    name: str = ""
    region: str = ""

    model_config = {"populate_by_name": True}


class BoundResources(BaseModel):
    redis: list[ResourceItem] = Field(default_factory=list)
    cdb: list[ResourceItem] = Field(default_factory=list)
    tdsqlc: list[TDSQLCResource] = Field(default_factory=list)
    tke: list[ResourceItem] = Field(default_factory=list)
    cls: list[CLSResource] = Field(default_factory=list)
    cos: list[ResourceItem] = Field(default_factory=list)


class Selection(BaseModel):
    """Current UI selection — used by preview-schema and starter.zip."""

    project: ProjectSpec = Field(default_factory=ProjectSpec)
    products: list[str] = Field(default_factory=list)
    disabled_tools: list[str] = Field(default_factory=list)
    resources: BoundResources = Field(default_factory=BoundResources)


class CloudCreds(BaseModel):
    secret_id: str = Field(alias="secretId")
    secret_key: str = Field(alias="secretKey")
    region: str = "ap-guangzhou"
    project_id: int | None = Field(default=None, alias="projectId")
    products: list[str] = Field(default_factory=list)

    model_config = {"populate_by_name": True}


class ListedResource(BaseModel):
    id: str
    name: str
    region: str = ""
    project_id: int | None = Field(default=None, alias="projectId")
    extra: dict[str, Any] = Field(default_factory=dict)

    model_config = {"populate_by_name": True}


class ListedTopic(BaseModel):
    id: str
    name: str
    logset_id: str = Field(alias="logsetId")

    model_config = {"populate_by_name": True}


class ListedLogset(BaseModel):
    id: str
    name: str
    region: str = ""
    topics: list[ListedTopic] = Field(default_factory=list)


class ResourceCatalog(BaseModel):
    redis: list[ListedResource] = Field(default_factory=list)
    cdb: list[ListedResource] = Field(default_factory=list)
    tdsqlc: list[ListedResource] = Field(default_factory=list)
    tke: list[ListedResource] = Field(default_factory=list)
    cls: list[ListedLogset] = Field(default_factory=list)
    cos: list[ListedResource] = Field(default_factory=list)
    errors: dict[str, str] = Field(default_factory=dict)
