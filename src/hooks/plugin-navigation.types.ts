export type PluginView = { kind: 'list' } | { kind: 'create' } | { kind: 'details'; id: string };

export type NavigateFn = (view: PluginView) => void;
