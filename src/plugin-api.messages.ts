export const Messages = {
  httpError: (status: number) => `Request failed (HTTP ${status}).`,
  unexpectedResponse: 'The migrations backend returned an unexpected response.',
};
