export const Messages = {
  uriRequired: 'Enter a connection URI.',
  uriScheme: (schemes: readonly string[]) =>
    `The URI must start with ${schemes.length > 1 ? `${schemes.slice(0, -1).join(', ')} or ${schemes[schemes.length - 1]}` : schemes[0]}.`,
  uriHost: 'The URI must include a host.',
  uriMultiHost: 'Use a single host: the primary server.',
};
