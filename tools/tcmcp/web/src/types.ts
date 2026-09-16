export type ProductId = "redis" | "cdb" | "tdsqlc" | "tke" | "cls" | "cos";

export interface ProductMeta {
  id: ProductId;
  name: string;
  description: string;
  color: string;
  tools: { name: string; description: string }[];
}

export interface Metadata {
  name: string;
  version: string;
  toolWarnThreshold: number;
  defaults: { name: string; target: string; region: string };
  products: ProductMeta[];
  regions: { id: string; name: string }[];
}

export interface ResourceItem {
  id: string;
  alias: string;
  name: string;
  region?: string;
  extra?: Record<string, unknown>;
}

export interface CLSTopic {
  id: string;
  alias: string;
  name: string;
}

export interface CLSResource {
  logsetId: string;
  logsetName: string;
  region?: string;
  topics: CLSTopic[];
}

export interface TDSQLCResource {
  clusterId: string;
  instanceId: string;
  alias: string;
  name: string;
  region?: string;
}

export interface BoundResources {
  redis: ResourceItem[];
  cdb: ResourceItem[];
  tdsqlc: TDSQLCResource[];
  tke: ResourceItem[];
  cls: CLSResource[];
  cos: ResourceItem[];
}

export interface Selection {
  project: {
    name: string;
    description: string;
    target: string;
    region: string;
  };
  products: string[];
  disabled_tools: string[];
  resources: BoundResources;
}

export interface SchemaTool {
  name: string;
  description: string;
  product: string;
  inputSchema: {
    type: string;
    properties?: Record<string, SchemaProp>;
    required?: string[];
  };
  annotations: { readOnlyHint: boolean };
  enabled: boolean;
  ready: boolean;
  skipReason: string;
}

export interface SchemaProp {
  type: string;
  description?: string;
  enum?: string[];
  default?: unknown;
  items?: { type: string };
}

export interface Preview {
  instructions: string;
  tools: SchemaTool[];
  skipped: SchemaTool[];
  grouped: Record<string, SchemaTool[]>;
  registered: string[];
  counts: { products: number; resources: number; tools: number; threshold: number };
  warn: boolean;
  warnMessage: string;
}

export interface ListedResource {
  id: string;
  name: string;
  region?: string;
  extra?: Record<string, unknown>;
}

export interface ListedLogset {
  id: string;
  name: string;
  region?: string;
  topics: { id: string; name: string; logsetId: string }[];
}

export interface ResourceCatalog {
  redis: ListedResource[];
  cdb: ListedResource[];
  tdsqlc: ListedResource[];
  tke: ListedResource[];
  cls: ListedLogset[];
  cos: ListedResource[];
  errors: Record<string, string>;
}
