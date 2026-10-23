import { ref, watch, onMounted, onBeforeUnmount } from 'vue'

// Hash routes work on the static preview and Hub server without rewrite rules.
export function useViewRoute(views, fallback) {
  const paths = new Map(views.map(view => ['/' + view.toLowerCase().replaceAll(' ', '-'), view]))
  const pathFor = view => [...paths].find(([, value]) => value === view)?.[0]
  const read = () => paths.get(window.location.hash.slice(1)) || fallback
  const view = ref(read())
  function receive() {
    view.value = read()
    if (!paths.has(window.location.hash.slice(1))) {
      history.replaceState(null, '', '#' + pathFor(view.value))
    }
  }
  watch(view, value => {
    const path = pathFor(value)
    if (path && window.location.hash !== '#' + path) window.location.hash = path
  })
  onMounted(() => { receive(); window.addEventListener('hashchange', receive) })
  onBeforeUnmount(() => window.removeEventListener('hashchange', receive))
  return view
}
