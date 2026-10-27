import { ref, onMounted, onBeforeUnmount } from 'vue';
export function useMobile() {
  const media = window.matchMedia('(max-width: 640px)');
  const mobile = ref(media.matches);
  const update = () => { mobile.value = media.matches; };
  onMounted(() => media.addEventListener('change', update));
  onBeforeUnmount(() => media.removeEventListener('change', update));
  return mobile;
}
