const PLUGIN_BASE_PATH = /^\/v1\/clusters\/([^/?#]+)\/plugins\/([^/?#]+)\/?$/;

// The host proxies plugin calls under /v1/clusters/{cluster}/plugins/{name}, so
// the Kubernetes cluster the plugin runs in is encoded in api.basePath.
export function clusterFromBasePath(basePath: string): string | undefined {
  const match = PLUGIN_BASE_PATH.exec(basePath);
  if (!match) return undefined;
  try {
    const cluster = decodeURIComponent(match[1]).trim();
    return cluster === '' ? undefined : cluster;
  } catch {
    return undefined;
  }
}
