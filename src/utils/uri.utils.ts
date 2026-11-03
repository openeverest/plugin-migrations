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
