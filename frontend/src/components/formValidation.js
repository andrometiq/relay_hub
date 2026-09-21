export function validateFields(fields, values) {
 const errors = {}
 for (const field of fields) {
  const value = values[field.name]
  if (field.required && (value == null || value === '' || (typeof value === 'string' && !value.trim()) || (Array.isArray(value) && !value.length))) errors[field.name] = `${field.label} is required.`
  if (field.url && value) {
   try { const url = new URL(value); if(url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || !['','/'].includes(url.pathname)) throw new Error(); }
   catch { errors[field.name] = 'Enter an HTTPS site URL without a path or credentials.' }
  }
 }
 return errors
}
