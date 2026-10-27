import { watch } from "vue";

export function usePendingRefresh(busy, refresh) {
  let pending = false;
  watch(busy, (value) => {
    if (!value && pending) {
      pending = false;
      refresh();
    }
  });
  return () => {
    if (busy.value) {
      pending = true;
      return;
    }
    return refresh();
  };
}
