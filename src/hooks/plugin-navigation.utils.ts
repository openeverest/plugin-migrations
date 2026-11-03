import { CREATE_SUB_PATH } from './plugin-navigation.constants';
import type { PluginView } from './plugin-navigation.types';

function decodeSegment(segment: string): string | undefined {
  try {
    return decodeURIComponent(segment);
  } catch {
    return undefined;
  }
}

export function viewFromSubPath(subPath: string | undefined): PluginView {
  const [segment = ''] = (subPath ?? '').split(/[?#]/)[0].split('/').filter(Boolean);
  if (segment === '') return { kind: 'list' };
  if (segment === CREATE_SUB_PATH) return { kind: 'create' };
  const id = decodeSegment(segment);
  return id ? { kind: 'details', id } : { kind: 'list' };
}

export function subPathFromView(view: PluginView): string {
  switch (view.kind) {
    case 'list':
      return '';
    case 'create':
      return CREATE_SUB_PATH;
    case 'details':
      return encodeURIComponent(view.id);
  }
}

export function pluginRouteBase(pluginName: string): string {
  return `/plugins/${encodeURIComponent(pluginName)}`;
}

export function urlForView(pluginName: string, view: PluginView): string {
  const subPath = subPathFromView(view);
  return subPath === '' ? pluginRouteBase(pluginName) : `${pluginRouteBase(pluginName)}/${subPath}`;
}

/** The plugin sub-path of a browser pathname, or undefined when it is outside the plugin route. */
export function subPathFromPathname(pathname: string, pluginName: string): string | undefined {
  const base = pluginRouteBase(pluginName);
  const index = pathname.indexOf(base);
  if (index < 0) return undefined;
  const rest = pathname.slice(index + base.length);
  if (rest !== '' && !rest.startsWith('/')) return undefined;
  return rest.replace(/^\/+/, '');
}

export function isSameView(a: PluginView, b: PluginView): boolean {
  return subPathFromView(a) === subPathFromView(b);
}
