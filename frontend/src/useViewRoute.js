import { ref, watch, onMounted, onBeforeUnmount } from 'vue'

const slug = view => view.toLowerCase().replaceAll(' ', '-')

// "#/sites/yrp" -> { view: "Sites", detail: "yrp" }. Redirects map old paths to current views.
export function parseRoute(hash, views, redirects = {}) {
  const [first = '', ...rest] = hash.replace(/^#\/?/, '').split('/')
  const name = redirects[first] || first
  const view = views.find(candidate => slug(candidate) === name)
  return view ? { view, detail: rest.length ? decodeURIComponent(rest.join('/')) : '' } : null
}

export function routePath(view, detail = '') {
  return '#/' + slug(view) + (detail ? '/' + encodeURIComponent(detail) : '')
}

// Hash routes work on the static preview and Hub server without rewrite rules.
export function useViewRoute(views, fallback, redirects = {}) {
  const view = ref(fallback), detail = ref('')
  function receive() {
    const route = parseRoute(window.location.hash, views, redirects) || { view: fallback, detail: '' }
    view.value = route.view
    detail.value = route.detail
    const path = routePath(route.view, route.detail)
    if (window.location.hash !== path) history.replaceState(null, '', path)
  }
  watch([view, detail], ([value, id]) => {
    const path = routePath(value, id)
    if (window.location.hash !== path) window.location.hash = path
  })
  onMounted(() => { receive(); window.addEventListener('hashchange', receive) })
  onBeforeUnmount(() => window.removeEventListener('hashchange', receive))
  return { view, detail }
}
