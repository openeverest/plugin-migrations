import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { initHostApi } from './__mocks__/host-api';
import { ApiError, apiFetch, apiFetchNoContent } from './plugin-api';

const fetchMock = vi.fn<PluginApi['fetch']>();

const parseGreeting = (body: unknown) =>
  typeof body === 'object' && body !== null && 'hello' in body && typeof body.hello === 'string' ? body.hello : undefined;

describe('apiFetch', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    initHostApi(fetchMock);
  });

  it('prefixes /api and returns the parsed body', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ hello: 'world' }), { status: 200 }));
    await expect(apiFetch('/status', { method: 'GET' }, parseGreeting)).resolves.toBe('world');
    expect(fetchMock).toHaveBeenCalledWith('/api/status', { method: 'GET' });
  });

  it('turns {"error": "..."} bodies into a typed ApiError', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: 'pause is not allowed' }), { status: 409 }));
    const error = await apiFetch('/migrations/x/pause', undefined, parseGreeting).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 409, message: 'pause is not allowed' });
  });

  it('falls back to short plain-text bodies and then to a generic message', async () => {
    fetchMock.mockResolvedValueOnce(new Response('upstream timeout', { status: 502 }));
    await expect(apiFetch('/x', undefined, parseGreeting)).rejects.toMatchObject({ status: 502, message: 'upstream timeout' });

    fetchMock.mockResolvedValueOnce(new Response('<html>Bad gateway</html>', { status: 502 }));
    await expect(apiFetch('/x', undefined, parseGreeting)).rejects.toMatchObject({
      status: 502,
      message: 'Request failed (HTTP 502).',
    });

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ error: '' }), { status: 400 }));
    await expect(apiFetch('/x', undefined, parseGreeting)).rejects.toMatchObject({ message: 'Request failed (HTTP 400).' });
  });

  it('rejects successful responses that fail the shape guard', async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ hello: 42 }), { status: 200 }));
    await expect(apiFetch('/x', undefined, parseGreeting)).rejects.toMatchObject({
      message: 'The migrations backend returned an unexpected response.',
    });

    fetchMock.mockResolvedValueOnce(new Response('not json', { status: 200 }));
    await expect(apiFetch('/x', undefined, parseGreeting)).rejects.toBeInstanceOf(ApiError);
  });

  it('accepts empty 204 responses for no-content calls', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await expect(apiFetchNoContent('/migrations/x', { method: 'DELETE' })).resolves.toBeUndefined();
  });
});
