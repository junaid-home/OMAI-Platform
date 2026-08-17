import type { Interceptor } from "@connectrpc/connect";

export type MaybePromise<T> = T | Promise<T>;
export type AccessTokenProvider = () => MaybePromise<string | null | undefined>;
export type HeaderProvider = () => MaybePromise<Readonly<Record<string, string | null | undefined>>>;

export interface MetadataOptions {
  readonly accessToken?: string | AccessTokenProvider;
  readonly tenantId?: string;
  readonly actorId?: string;
  readonly headers?: Readonly<Record<string, string>> | HeaderProvider;
}

const invalidHeaderValue = /[\r\n\0]/u;
const validHeaderName = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u;
const reservedHeaders = new Set(["authorization", "x-omai-actor-id", "x-omai-tenant-id"]);

function checkedValue(name: string, value: string): string {
  const trimmed = value.trim();
  if (trimmed.length === 0 || trimmed.length > 8_192 || invalidHeaderValue.test(trimmed)) {
    throw new TypeError(`Invalid value for OMAI header ${name}`);
  }
  return trimmed;
}

async function resolveToken(source: MetadataOptions["accessToken"]): Promise<string | undefined> {
  const value = typeof source === "function" ? await source() : source;
  if (value == null || value.trim() === "") {
    return undefined;
  }
  const token = checkedValue("Authorization", value);
  if (/\s/u.test(token)) {
    throw new TypeError("OMAI access tokens cannot contain whitespace");
  }
  return token;
}

async function resolveHeaders(source: MetadataOptions["headers"]): Promise<Readonly<Record<string, string | null | undefined>>> {
  if (source == null) {
    return {};
  }
  return typeof source === "function" ? await source() : source;
}

/** Add fresh identity metadata for every unary or streaming request. */
export function createMetadataInterceptor(options: MetadataOptions): Interceptor {
  return (next) => async (request) => {
    const [token, dynamicHeaders] = await Promise.all([
      resolveToken(options.accessToken),
      resolveHeaders(options.headers),
    ]);

    if (token !== undefined) {
      request.header.set("Authorization", `Bearer ${token}`);
    }
    if (options.tenantId !== undefined) {
      request.header.set("X-OMAI-Tenant-ID", checkedIdentity("X-OMAI-Tenant-ID", options.tenantId));
    }
    if (options.actorId !== undefined) {
      request.header.set("X-OMAI-Actor-ID", checkedIdentity("X-OMAI-Actor-ID", options.actorId));
    }
    for (const [name, rawValue] of Object.entries(dynamicHeaders)) {
      if (!validHeaderName.test(name)) {
        throw new TypeError(`Invalid OMAI header name ${JSON.stringify(name)}`);
      }
      if (reservedHeaders.has(name.toLowerCase())) {
        throw new TypeError(`Use the dedicated OMAI metadata option for ${name}`);
      }
      if (rawValue != null) {
        request.header.set(name, checkedValue(name, rawValue));
      }
    }
    return next(request);
  };
}

function checkedIdentity(name: string, value: string): string {
  const checked = checkedValue(name, value);
  if (checked.length > 256) {
    throw new TypeError(`Invalid value for OMAI header ${name}`);
  }
  return checked;
}
