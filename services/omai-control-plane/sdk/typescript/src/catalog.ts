import { fromJson, toJson, type JsonValue } from "@bufbuild/protobuf";
import { StructSchema } from "@bufbuild/protobuf/wkt";
import type { CallOptions, Client } from "@connectrpc/connect";
import type { ModelCatalogService } from "./gen/uab/v1/model_catalog_pb.js";

type JSONObject = { readonly [key: string]: JsonValue };

export interface ModelProvider {
  readonly id: string;
  readonly sourceId: string;
  readonly name: string;
  readonly api: string;
  readonly npm: string;
  readonly documentation: string;
  readonly environmentVariables: readonly string[];
  readonly modelCount: number;
  readonly connected: boolean;
  readonly runtimeId: string;
  readonly runtimeIds: readonly string[];
}

export interface ModelLimits {
  readonly context: number;
  readonly input: number;
  readonly output: number;
}

export interface ModelModalities {
  readonly input: readonly string[];
  readonly output: readonly string[];
}

export interface ModelReasoningOption {
  readonly type: string;
  readonly values: readonly (string | null)[];
  readonly minimum: number | undefined;
  readonly maximum: number | undefined;
}

export interface ModelCostBand {
  readonly type: string;
  readonly size: number;
}

export interface ModelUnitCost {
  readonly input: number;
  readonly output: number;
  readonly cacheRead: number | undefined;
  readonly cacheWrite: number | undefined;
  readonly inputAudio: number | undefined;
  readonly outputAudio: number | undefined;
  readonly reasoning: number | undefined;
}

export interface ModelCostTier extends ModelUnitCost {
  readonly tier: ModelCostBand;
}

export interface ModelCost extends ModelUnitCost {
  readonly tiers: readonly ModelCostTier[];
  readonly contextOver200K: ModelUnitCost | undefined;
}

export interface ModelProviderOverride {
  readonly npm: string;
  readonly api: string;
}

export interface ModelExperimentalMode {
  readonly cost: ModelCost | undefined;
  readonly provider: {
    readonly body: Readonly<Record<string, JsonValue>>;
    readonly headers: Readonly<Record<string, string>>;
  } | undefined;
}

export interface CatalogModel {
  readonly id: string;
  readonly name: string;
  readonly description: string;
  readonly family: string;
  readonly knowledge: string;
  readonly providerId: string;
  readonly sourceProviderId: string;
  readonly runtimeId: string;
  readonly runtimeIds: readonly string[];
  readonly ready: boolean;
  readonly free: boolean;
  readonly unavailableReason: string;
  readonly status: string;
  readonly mode: string;
  readonly releaseDate: string;
  readonly lastUpdated: string;
  readonly attachment: boolean;
  readonly reasoning: boolean;
  readonly temperature: boolean;
  readonly toolCall: boolean;
  readonly structuredOutput: boolean;
  readonly openWeights: boolean;
  readonly interleaved: JsonValue | undefined;
  readonly reasoningOptions: readonly ModelReasoningOption[];
  readonly modalities: ModelModalities;
  readonly cost: ModelCost | undefined;
  readonly provider: ModelProviderOverride | undefined;
  readonly experimentalModes: Readonly<Record<string, ModelExperimentalMode>>;
  readonly options: Readonly<Record<string, JsonValue>>;
  readonly headers: Readonly<Record<string, string>>;
  readonly limits: ModelLimits;
}

export interface CatalogPage {
  readonly schemaVersion: string;
  readonly providers: readonly ModelProvider[];
  readonly connectedProviderIds: readonly string[];
  readonly defaults: Readonly<Record<string, string>>;
  readonly models: readonly CatalogModel[];
  readonly total: number;
  readonly offset: number;
  readonly nextOffset: number;
}

export interface ProviderPage {
  readonly schemaVersion: string;
  readonly providers: readonly ModelProvider[];
  readonly connectedProviderIds: readonly string[];
  readonly defaults: Readonly<Record<string, string>>;
}

export interface ListProvidersInput {
  readonly query?: string;
  readonly runtimeId?: string;
  readonly connectedOnly?: boolean;
  readonly limit?: number;
}

export interface ListModelsInput {
  readonly providerId?: string;
  readonly runtimeId?: string;
  readonly offset?: number;
  readonly limit?: number;
}

export interface SearchModelsInput extends ListModelsInput {
  readonly query: string;
}

export interface GetModelInput {
  readonly id: string;
  readonly providerId?: string;
}

export interface OMAIModelCatalog {
  getCatalog(options?: CallOptions): Promise<CatalogPage>;
  listProviders(input?: ListProvidersInput, options?: CallOptions): Promise<ProviderPage>;
  listModels(input?: ListModelsInput, options?: CallOptions): Promise<CatalogPage>;
  searchModels(input: SearchModelsInput, options?: CallOptions): Promise<CatalogPage>;
  getModel(input: GetModelInput, options?: CallOptions): Promise<CatalogModel>;
}

export class CatalogSchemaError extends Error {
  constructor(path: string, expected: string) {
    super(`Invalid model catalog at ${path}: expected ${expected}`);
    this.name = "CatalogSchemaError";
  }
}

export function createModelCatalog(client: Client<typeof ModelCatalogService>): OMAIModelCatalog {
  return Object.freeze({
    async getCatalog(options?: CallOptions): Promise<CatalogPage> {
      return parseCatalogPage(toJson(StructSchema, await client.getCatalog(structRequest({}), options)));
    },
    async listProviders(input: ListProvidersInput = {}, options?: CallOptions): Promise<ProviderPage> {
      const query = checkedQuery(input.query ?? "", true);
      const value = await client.listProviders(structRequest({
        query,
        runtime_id: optionalIdentifier(input.runtimeId, "runtime id"),
        connected_only: input.connectedOnly ?? false,
        limit: boundedInteger(input.limit, 100, 1, 1_000, "limit"),
      }), options);
      return parseProviderPage(toJson(StructSchema, value));
    },
    async listModels(input: ListModelsInput = {}, options?: CallOptions): Promise<CatalogPage> {
      const value = await client.listModels(structRequest(modelPageRequest(input)), options);
      return parseCatalogPage(toJson(StructSchema, value));
    },
    async searchModels(input: SearchModelsInput, options?: CallOptions): Promise<CatalogPage> {
      const query = checkedQuery(input.query, false);
      const value = await client.searchModels(structRequest({ ...modelPageRequest(input), query }), options);
      return parseCatalogPage(toJson(StructSchema, value));
    },
    async getModel(input: GetModelInput, options?: CallOptions): Promise<CatalogModel> {
      const id = checkedIdentifier(input.id, "model id");
      const value = await client.getModel(structRequest({
        id,
        provider_id: input.providerId === undefined ? "" : checkedIdentifier(input.providerId, "provider id"),
      }), options);
      const root = record(toJson(StructSchema, value), "$response");
      return parseModel(record(root.model, "$response.model"), "$response.model");
    },
  });
}

function modelPageRequest(input: ListModelsInput): JSONObject {
  return {
    provider_id: optionalIdentifier(input.providerId, "provider id"),
    runtime_id: optionalIdentifier(input.runtimeId, "runtime id"),
    offset: boundedInteger(input.offset, 0, 0, 10_000_000, "offset"),
    limit: boundedInteger(input.limit, 100, 1, 10_000, "limit"),
  };
}

function structRequest(value: JSONObject) {
  return fromJson(StructSchema, value);
}

function boundedInteger(value: number | undefined, fallback: number, minimum: number, maximum: number, name: string): number {
  const resolved = value ?? fallback;
  if (!Number.isSafeInteger(resolved) || resolved < minimum || resolved > maximum) {
    throw new RangeError(`${name} must be an integer between ${minimum} and ${maximum}`);
  }
  return resolved;
}

function checkedIdentifier(value: string, name: string): string {
  const normalized = value.trim();
  if (normalized.length === 0 || normalized.length > 512 || normalized !== value || /[\r\n\0]/u.test(value)) {
    throw new TypeError(`Invalid ${name}`);
  }
  return normalized;
}

function optionalIdentifier(value: string | undefined, name: string): string {
  return value === undefined ? "" : checkedIdentifier(value, name);
}

function checkedQuery(value: string, allowEmpty: boolean): string {
  const normalized = value.trim();
  if ((!allowEmpty && normalized.length === 0) || normalized.length > 512 || /[\r\n\0]/u.test(value)) {
    throw new RangeError("Model search query must contain between 1 and 512 safe characters");
  }
  return normalized;
}

export function parseCatalogPage(value: JsonValue): CatalogPage {
  const root = record(value, "$response");
  const models = array(root.models, "$response.models").map((item, index) => parseModel(record(item, `$response.models[${index}]`), `$response.models[${index}]`));
  return Object.freeze({
    ...parseCommon(root),
    models: Object.freeze(models),
    total: nonNegativeInteger(root.total, "$response.total", models.length),
    offset: nonNegativeInteger(root.offset, "$response.offset", 0),
    nextOffset: nonNegativeInteger(root.next_offset, "$response.next_offset", 0),
  });
}

export function parseProviderPage(value: JsonValue): ProviderPage {
  return Object.freeze(parseCommon(record(value, "$response")));
}

function parseCommon(root: JSONObject): ProviderPage {
  const providers = array(root.providers, "$response.providers").map((item, index) => parseProvider(record(item, `$response.providers[${index}]`), `$response.providers[${index}]`));
  return {
    schemaVersion: requiredString(root.schema_version, "$response.schema_version"),
    providers: Object.freeze(providers),
    connectedProviderIds: Object.freeze(stringArray(root.connected, "$response.connected")),
    defaults: Object.freeze(stringMap(root.default, "$response.default")),
  };
}

function parseProvider(value: JSONObject, path: string): ModelProvider {
  return Object.freeze({
    id: requiredString(value.id, `${path}.id`),
    sourceId: optionalString(value.source_id, `${path}.source_id`),
    name: requiredString(value.name, `${path}.name`),
    api: optionalString(value.api, `${path}.api`),
    npm: optionalString(value.npm, `${path}.npm`),
    documentation: optionalString(value.doc, `${path}.doc`),
    environmentVariables: Object.freeze(stringArray(value.env, `${path}.env`)),
    modelCount: nonNegativeInteger(value.model_count, `${path}.model_count`, 0),
    connected: optionalBoolean(value.connected, `${path}.connected`),
    runtimeId: optionalString(value.runtime_id, `${path}.runtime_id`),
    runtimeIds: Object.freeze(runtimeIds(value, path)),
  });
}

function parseModel(value: JSONObject, path: string): CatalogModel {
  const reasoningOptions = array(value.reasoning_options, `${path}.reasoning_options`).map((item, index) => {
    const option = record(item, `${path}.reasoning_options[${index}]`);
    return Object.freeze({
      type: requiredString(option.type, `${path}.reasoning_options[${index}].type`),
      values: Object.freeze(nullableStringArray(option.values, `${path}.reasoning_options[${index}].values`)),
      minimum: optionalNumber(option.min, `${path}.reasoning_options[${index}].min`),
      maximum: optionalNumber(option.max, `${path}.reasoning_options[${index}].max`),
    });
  });
  const modalities = optionalRecord(value.modalities, `${path}.modalities`);
  const limits = optionalRecord(value.limits, `${path}.limits`);
  const experimental = optionalRecord(value.experimental, `${path}.experimental`);

  return Object.freeze({
    id: requiredString(value.id, `${path}.id`),
    name: requiredString(value.name, `${path}.name`),
    description: optionalString(value.description, `${path}.description`),
    family: optionalString(value.family, `${path}.family`),
    knowledge: optionalString(value.knowledge, `${path}.knowledge`),
    providerId: requiredString(value.provider_id, `${path}.provider_id`),
    sourceProviderId: optionalString(value.source_provider_id, `${path}.source_provider_id`),
    runtimeId: optionalString(value.runtime_id, `${path}.runtime_id`),
    runtimeIds: Object.freeze(runtimeIds(value, path)),
    ready: optionalBoolean(value.ready, `${path}.ready`),
    free: optionalBoolean(value.free, `${path}.free`),
    unavailableReason: optionalString(value.unavailable_reason, `${path}.unavailable_reason`),
    status: optionalString(value.status, `${path}.status`),
    mode: optionalString(value.mode, `${path}.mode`),
    releaseDate: optionalString(value.release_date, `${path}.release_date`),
    lastUpdated: optionalString(value.last_updated, `${path}.last_updated`),
    attachment: optionalBoolean(value.attachment, `${path}.attachment`),
    reasoning: optionalBoolean(value.reasoning, `${path}.reasoning`),
    temperature: optionalBoolean(value.temperature, `${path}.temperature`),
    toolCall: optionalBoolean(value.tool_call, `${path}.tool_call`),
    structuredOutput: optionalBoolean(value.structured_output, `${path}.structured_output`),
    openWeights: optionalBoolean(value.open_weights, `${path}.open_weights`),
    interleaved: value.interleaved,
    reasoningOptions: Object.freeze(reasoningOptions),
    modalities: Object.freeze({
      input: Object.freeze(stringArray(modalities?.input, `${path}.modalities.input`)),
      output: Object.freeze(stringArray(modalities?.output, `${path}.modalities.output`)),
    }),
    cost: parseCost(value.cost, `${path}.cost`),
    provider: parseProviderOverride(value.provider, `${path}.provider`),
    experimentalModes: Object.freeze(parseExperimentalModes(experimental?.modes, `${path}.experimental.modes`)),
    options: Object.freeze(jsonMap(value.options, `${path}.options`)),
    headers: Object.freeze(stringMap(value.headers, `${path}.headers`)),
    limits: Object.freeze({
      context: nonNegativeInteger(limits?.context, `${path}.limits.context`, 0),
      input: nonNegativeInteger(limits?.input, `${path}.limits.input`, 0),
      output: nonNegativeInteger(limits?.output, `${path}.limits.output`, 0),
    }),
  });
}

function runtimeIds(value: JSONObject, path: string): string[] {
  const primary = optionalString(value.runtime_id, `${path}.runtime_id`);
  const result = [...stringArray(value.runtime_ids, `${path}.runtime_ids`)];
  if (primary !== "" && !result.includes(primary)) {
    result.unshift(primary);
  }
  return result;
}

function parseCost(value: JsonValue | undefined, path: string): ModelCost | undefined {
  const source = optionalRecord(value, path);
  if (source === undefined) {
    return undefined;
  }
  const tiers = array(source.tiers, `${path}.tiers`).map((item, index) => {
    const tier = record(item, `${path}.tiers[${index}]`);
    const band = record(tier.tier, `${path}.tiers[${index}].tier`);
    return Object.freeze({
      ...parseUnitCost(tier, `${path}.tiers[${index}]`),
      tier: Object.freeze({
        type: requiredString(band.type, `${path}.tiers[${index}].tier.type`),
        size: nonNegativeNumber(band.size, `${path}.tiers[${index}].tier.size`, 0),
      }),
    });
  });
  const extended = optionalRecord(source.context_over_200k, `${path}.context_over_200k`);
  return Object.freeze({
    ...parseUnitCost(source, path),
    tiers: Object.freeze(tiers),
    contextOver200K: extended === undefined ? undefined : Object.freeze(parseUnitCost(extended, `${path}.context_over_200k`)),
  });
}

function parseUnitCost(source: JSONObject, path: string): ModelUnitCost {
  return {
    input: nonNegativeNumber(source.input, `${path}.input`, 0),
    output: nonNegativeNumber(source.output, `${path}.output`, 0),
    cacheRead: optionalNonNegativeNumber(source.cache_read, `${path}.cache_read`),
    cacheWrite: optionalNonNegativeNumber(source.cache_write, `${path}.cache_write`),
    inputAudio: optionalNonNegativeNumber(source.input_audio, `${path}.input_audio`),
    outputAudio: optionalNonNegativeNumber(source.output_audio, `${path}.output_audio`),
    reasoning: optionalNonNegativeNumber(source.reasoning, `${path}.reasoning`),
  };
}

function parseProviderOverride(value: JsonValue | undefined, path: string): ModelProviderOverride | undefined {
  const source = optionalRecord(value, path);
  return source === undefined ? undefined : Object.freeze({
    npm: optionalString(source.npm, `${path}.npm`),
    api: optionalString(source.api, `${path}.api`),
  });
}

function parseExperimentalModes(value: JsonValue | undefined, path: string): Record<string, ModelExperimentalMode> {
  const modes = optionalRecord(value, path);
  if (modes === undefined) {
    return {};
  }
  const result: Record<string, ModelExperimentalMode> = {};
  for (const [name, rawMode] of Object.entries(modes)) {
    const mode = record(rawMode, `${path}.${name}`);
    const provider = optionalRecord(mode.provider, `${path}.${name}.provider`);
    result[name] = Object.freeze({
      cost: parseCost(mode.cost, `${path}.${name}.cost`),
      provider: provider === undefined ? undefined : Object.freeze({
        body: Object.freeze(jsonMap(provider.body, `${path}.${name}.provider.body`)),
        headers: Object.freeze(stringMap(provider.headers, `${path}.${name}.provider.headers`)),
      }),
    });
  }
  return result;
}

function record(value: JsonValue | undefined, path: string): JSONObject {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new CatalogSchemaError(path, "object");
  }
  return value;
}

function optionalRecord(value: JsonValue | undefined, path: string): JSONObject | undefined {
  return value === undefined || value === null ? undefined : record(value, path);
}

function array(value: JsonValue | undefined, path: string): readonly JsonValue[] {
  if (value === undefined || value === null) {
    return [];
  }
  if (!Array.isArray(value)) {
    throw new CatalogSchemaError(path, "array");
  }
  return value;
}

function requiredString(value: JsonValue | undefined, path: string): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new CatalogSchemaError(path, "non-empty string");
  }
  return value;
}

function optionalString(value: JsonValue | undefined, path: string): string {
  if (value === undefined || value === null) {
    return "";
  }
  if (typeof value !== "string") {
    throw new CatalogSchemaError(path, "string");
  }
  return value;
}

function optionalBoolean(value: JsonValue | undefined, path: string): boolean {
  if (value === undefined || value === null) {
    return false;
  }
  if (typeof value !== "boolean") {
    throw new CatalogSchemaError(path, "boolean");
  }
  return value;
}

function optionalNumber(value: JsonValue | undefined, path: string): number | undefined {
  if (value === undefined || value === null) {
    return undefined;
  }
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new CatalogSchemaError(path, "finite number");
  }
  return value;
}

function optionalNonNegativeNumber(value: JsonValue | undefined, path: string): number | undefined {
  const result = optionalNumber(value, path);
  if (result !== undefined && result < 0) {
    throw new CatalogSchemaError(path, "non-negative number");
  }
  return result;
}

function nonNegativeNumber(value: JsonValue | undefined, path: string, fallback: number): number {
  const result = optionalNumber(value, path) ?? fallback;
  if (result < 0) {
    throw new CatalogSchemaError(path, "non-negative number");
  }
  return result;
}

function nonNegativeInteger(value: JsonValue | undefined, path: string, fallback: number): number {
  const result = nonNegativeNumber(value, path, fallback);
  if (!Number.isSafeInteger(result)) {
    throw new CatalogSchemaError(path, "non-negative safe integer");
  }
  return result;
}

function stringArray(value: JsonValue | undefined, path: string): string[] {
  return array(value, path).map((item, index) => {
    if (typeof item !== "string") {
      throw new CatalogSchemaError(`${path}[${index}]`, "string");
    }
    return item;
  });
}

function nullableStringArray(value: JsonValue | undefined, path: string): (string | null)[] {
  return array(value, path).map((item, index) => {
    if (item !== null && typeof item !== "string") {
      throw new CatalogSchemaError(`${path}[${index}]`, "string or null");
    }
    return item;
  });
}

function stringMap(value: JsonValue | undefined, path: string): Record<string, string> {
  const source = optionalRecord(value, path);
  if (source === undefined) {
    return {};
  }
  const result: Record<string, string> = {};
  for (const [name, item] of Object.entries(source)) {
    if (typeof item !== "string") {
      throw new CatalogSchemaError(`${path}.${name}`, "string");
    }
    result[name] = item;
  }
  return result;
}

function jsonMap(value: JsonValue | undefined, path: string): Record<string, JsonValue> {
  const source = optionalRecord(value, path);
  return source === undefined ? {} : { ...source };
}
