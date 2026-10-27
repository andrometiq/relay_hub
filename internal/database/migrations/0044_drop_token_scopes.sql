-- A token is bound to one site and may call every operation for it; Core enforces permissions.
ALTER TABLE relay_api_tokens DROP COLUMN scopes;
