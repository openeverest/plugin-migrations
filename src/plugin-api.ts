import type { PluginApi } from '@openeverest/plugin-sdk';
import { Messages } from './plugin-api.messages';
import { isRecord } from './utils/guards.utils';

// Host-provided values, captured once in register(api) so any module can use them.
export let pluginFetch: PluginApi['fetch'];
export let cssNonce: PluginApi['cssNonce'];
export let basePath: PluginApi['basePath'];

export function initPluginApi(api: PluginApi): void {
  pluginFetch = api.fetch.bind(api);
  cssNonce = api.cssNonce;
  basePath = api.basePath;
}

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

export type ResponseParser<T> = (body: unknown) => T | undefined;

const MAX_PLAIN_ERROR_LENGTH = 300;

function parseJson(text: string): unknown {
  try {
    const value: unknown = JSON.parse(text);
    return value;
  } catch {
    return undefined;
  }
}

async function readErrorMessage(response: Response): Promise<string> {
  const text = (await response.text().catch(() => '')).trim();
  const body = parseJson(text);
  if (isRecord(body) && typeof body.error === 'string' && body.error.trim() !== '') {
    return body.error.trim();
  }
  if (text !== '' && body === undefined && text.length <= MAX_PLAIN_ERROR_LENGTH && !text.startsWith('<')) {
    return text;
  }
  return Messages.httpError(response.status);
}

async function request(path: string, init?: RequestInit): Promise<Response> {
  // Goes through api.fetch() so the host proxy validates the session and
  // forwards the user's JWT to the plugin backend.
  const response = await pluginFetch(`/api${path}`, init);
  if (!response.ok) throw new ApiError(response.status, await readErrorMessage(response));
  return response;
}

export async function apiFetch<T>(path: string, init: RequestInit | undefined, parse: ResponseParser<T>): Promise<T> {
  const response = await request(path, init);
  const text = await response.text().catch(() => '');
  const parsed = parse(parseJson(text));
  if (parsed === undefined) throw new ApiError(response.status, Messages.unexpectedResponse);
  return parsed;
}

export async function apiFetchNoContent(path: string, init?: RequestInit): Promise<void> {
  await request(path, init);
}

export function jsonRequest(method: string, body: unknown, signal?: AbortSignal): RequestInit {
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  };
}
