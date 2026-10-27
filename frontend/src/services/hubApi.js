let csrf = '';

// Error bodies are rendered as text, never markup. Bound and redact diagnostics.
export function safeServerError(text, fallback) {
  let value = text.trim();
  try {
    const data = JSON.parse(value);
    value = data.message || data.error?.message || data.error || data.Error || '';
  } catch { /* Go handlers also return plain text. */ }
  if (typeof value !== 'string' || !value || /<\/?(?:html|body|script|!doctype)/i.test(value)) return fallback;
  return value.replace(/(?:Bearer\s+)[\w.\-]+/gi, 'Bearer [redacted]')
    .replace(/((?:access_token|api_secret|app_secret|password|authorization|callback_secret)\s*[=:]\s*)[^\s,;]+/gi, '$1[redacted]')
    .replace(/[\u0000-\u0008\u000b-\u001f]/g, '').slice(0, 800);
}

export async function hubApi(path, body) {
  const write = body !== undefined;
  let response;
  try {
    response = await fetch(`./api/${path}`, {
      method: write ? 'POST' : 'GET', credentials: 'same-origin',
      headers: { Accept: 'application/json', ...(write ? { 'Content-Type': 'application/json', 'X-Relay-CSRF': csrf } : {}) },
      ...(write ? { body: JSON.stringify(body) } : {}),
    });
  } catch { throw Error('Connection lost. Your changes are still here. Check the connection before retrying.'); }
  if (!response.ok) {
    const fallback = response.status === 401 ? 'Your session has expired. Sign in again to continue.'
      : response.status === 403 ? 'You do not have permission for this action, or your session needs refreshing.'
      : `Request failed (${response.status}). Refresh the current state before retrying.`;
    const detail = safeServerError(await response.text(), '');
    throw Error(detail ? `${fallback} ${detail}` : fallback);
  }
  if (response.status === 204) return {};
  const data = await response.json();
  if (data.csrf) csrf = data.csrf;
  return data;
}
