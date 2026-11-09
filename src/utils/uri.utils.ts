import { Messages } from './uri.messages';

const MASKED_PASSWORD = '****';
const SCHEME = /^([a-z][a-z0-9+.-]*:\/\/)/i;

// Strips the password from a connection URI before it is displayed. When the
// boundary of the user info is ambiguous (unencoded '@' in a password), we mask
// up to the last '@' so a password is never echoed, even if that hides more.
export function maskUriCredentials(uri: string): string {
  const trimmed = uri.trim();
  const scheme = SCHEME.exec(trimmed)?.[1] ?? '';
  const rest = trimmed.slice(scheme.length);
  const at = rest.lastIndexOf('@');
  if (at < 0) return trimmed;
  const userInfo = rest.slice(0, at);
  const colon = userInfo.indexOf(':');
  const user = colon < 0 ? userInfo : userInfo.slice(0, colon);
  const maskedUserInfo = colon < 0 ? user : `${user}:${MASKED_PASSWORD}`;
  return `${scheme}${maskedUserInfo}@${rest.slice(at + 1)}`;
}

/** Validates a single-host URI with one of schemes (e.g. "mysql://"). */
export function validateSingleHostUri(uri: string, schemes: readonly string[]): string | undefined {
  const trimmed = uri.trim();
  if (trimmed === '') return Messages.uriRequired;
  const scheme = schemes.find((candidate) => trimmed.toLowerCase().startsWith(candidate));
  if (scheme === undefined) return Messages.uriScheme(schemes);
  const rest = trimmed.slice(scheme.length);
  const authorityEnd = rest.search(/[/?#]/);
  const authority = authorityEnd < 0 ? rest : rest.slice(0, authorityEnd);
  const hosts = authority.slice(authority.lastIndexOf('@') + 1);
  if (hosts.trim() === '') return Messages.uriHost;
  if (hosts.includes(',')) return Messages.uriMultiHost;
  return undefined;
}
