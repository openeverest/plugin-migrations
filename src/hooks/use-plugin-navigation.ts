import { useCallback, useEffect, useState } from 'react';
import type { NavigateFn, PluginView } from './plugin-navigation.types';
import { isSameView, subPathFromPathname, urlForView, viewFromSubPath } from './plugin-navigation.utils';

// Navigation goes through the browser history so the host router (mounted at
// /plugins/<pluginName>/*) stays in sync. The local view state is a fallback
// for hosts that do not re-render the route on popstate.
export function usePluginNavigation(pluginName: string, subPath: string | undefined) {
  const [view, setView] = useState<PluginView>(() => viewFromSubPath(subPath));

  useEffect(() => {
    const next = viewFromSubPath(subPath);
    setView((current) => (isSameView(current, next) ? current : next));
  }, [subPath]);

  useEffect(() => {
    const onPopState = () => {
      const pathSubPath = subPathFromPathname(window.location.pathname, pluginName);
      if (pathSubPath === undefined) return;
      const next = viewFromSubPath(pathSubPath);
      setView((current) => (isSameView(current, next) ? current : next));
    };
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, [pluginName]);

  const navigate = useCallback<NavigateFn>(
    (next) => {
      setView(next);
      const url = urlForView(pluginName, next);
      if (window.location.pathname !== url) {
        window.history.pushState(window.history.state, '', url);
        window.dispatchEvent(new PopStateEvent('popstate', { state: window.history.state }));
      }
    },
    [pluginName],
  );

  return { view, navigate };
}
