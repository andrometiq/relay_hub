export function validateFields(fields, values) {
 const errors = {}
 for (const field of fields) {
  const value = values[field.name]
  const empty = value == null || value === '' || (typeof value === 'string' && !value.trim()) || (Array.isArray(value) && !value.length)
  if (field.required && empty) { errors[field.name] = `${field.label} is required.`; continue }
  if (empty) continue
  if (field.type === 'Int' && (!Number.isSafeInteger(Number(value)) || field.min != null && Number(value) < field.min || field.max != null && Number(value) > field.max)) errors[field.name] = `Enter a valid ${field.label.toLowerCase()}${field.min != null && field.max != null ? ` between ${field.min} and ${field.max}` : ''}.`
  if (field.maxLength && String(value).length > field.maxLength) errors[field.name] = `${field.label} must contain at most ${field.maxLength} characters.`
  if (field.minLength && String(value).length < field.minLength) errors[field.name] = `Use at least ${field.minLength} characters.`
  if (field.url) {
   try { const url = new URL(value); if(!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || !['','/'].includes(url.pathname)) throw new Error(); }
   catch { errors[field.name] = 'Enter an HTTP or HTTPS site URL, optionally with a port, without a path, query, fragment, or credentials.' }
  }
 }
 return errors
}
export function positiveInteger(value, label) {
 if (!/^\d+$/.test(String(value)) || !Number.isSafeInteger(Number(value)) || Number(value) <= 0) throw Error('Enter a valid ' + label + '.')
 return Number(value)
}
export function coordinates(latitude, longitude) {
 if (latitude === '' || longitude === '' || !Number.isFinite(Number(latitude)) || !Number.isFinite(Number(longitude)) || Math.abs(Number(latitude)) > 90 || Math.abs(Number(longitude)) > 180) throw Error('Enter latitude from −90 to 90 and longitude from −180 to 180.')
 return { latitude: Number(latitude), longitude: Number(longitude) }
}
