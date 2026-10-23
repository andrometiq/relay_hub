let csrf = "";
export async function hubApi(path, body) {
  const write = body !== undefined;
  let response;
  try {
    response = await fetch(`./api/${path}`, {
      method: write ? "POST" : "GET",
      credentials: "same-origin",
      headers: {
        Accept: "application/json",
        ...(write
          ? { "Content-Type": "application/json", "X-Relay-CSRF": csrf }
          : {}),
      },
      ...(write ? { body: JSON.stringify(body) } : {}),
    });
  } catch {
    throw Error(
      "Connection lost. Your changes are still here. Check the connection before retrying.",
    );
  }
  if (response.status === 401)
    throw Error("Your session has expired. Sign in again to continue.");
  if (response.status === 403)
    throw Error(
      "You do not have permission for this action, or your session needs refreshing.",
    );
  if (!response.ok)
    throw Error(
      "The request was not confirmed. Check the configuration before retrying.",
    );
  if (response.status === 204) return {};
  const data = await response.json().catch(() => ({}));
  if (data.csrf) csrf = data.csrf;
  return data;
}
