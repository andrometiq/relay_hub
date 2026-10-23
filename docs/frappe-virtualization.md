# Frappe/ERPNext virtualization: source review and adoption

Reviewed the locally installed Frappe report/list source, frappe-datatable and hyperlist, plus the official DataTable source/docs. Relay Hub uses PrimeVue in unstyled mode and its own virtualized rows; the earlier Frappe component library was removed from the runtime dependency graph.

Primary sources:
- https://github.com/frappe/datatable/blob/master/src/body-renderer.js
- https://frappe.io/datatable/docs/configuration
- Local Frappe `frappe/public/js/frappe/views/reports/report_view.js`: constructs DataTable with fixed cellHeight.
- Local DataTable `src/body-renderer.js`: imports HyperList, passes width/height/itemHeight/total/generate and refreshes an existing renderer.
- Local Frappe `frappe/public/js/frappe/list/base_list.js`: pages data separately (100 desktop / 20 smaller-screen initial page length in this version).

Relay now pins the same `hyperlist@1.0.0` dependency in Core and Hub. `FrappeRows.vue` passes fixed-height rows to the upstream engine and bridges its generated DOM nodes to existing Vue component slots through Teleport. HyperList owns visible-range calculation, cached rows, row placement and scroll rendering. The old hand-written visible-range renderers are removed/replaced. The adapter destroys the upstream RAF loop when hidden/unmounted and resumes on visibility. It does not replace the dependency's internal algorithm.

Data loading remains separate: existing cursor paging bounds the buffer at 200 rows, avoiding passing a million allocated records to either renderer. Table rows default to 48px; conversation/action rows retain their appropriate larger heights. This reuses the scrolling engine, not the whole spreadsheet/editor UI of DataTable. Vue cell components and Relay's UI theme remain intact.

Core WhatsApp polling now schedules the next cycle after the prior one completes. Hidden inbox polling is skipped; active-call signaling is preserved. No other Codex or Frappe bench services were stopped.

## Focused comparison

A generated million-record source was accessed through 21 page requests (50 rows each) using the old built harness and the new upstream-based harness. At a 1440×900 viewport, one scripted 20-scroll run measured about 160ms vs 149ms browser TaskDuration. A follow-up using fresh browser contexts and forced garbage collection measured 3.1MB vs 3.2MB retained JS heap. Both had zero idle DOM mutations; the new renderer scheduled zero RAF callbacks after the test hid the document. Reverse paging and live unread/search/mobile checks passed.

These are small synthetic diagnostics, not a claim of significant overall CPU or RAM reduction. Heap is effectively similar and timings are noisy. A host process snapshot also showed large unrelated Code, esbuild, MariaDB and Frappe processes; it does not establish which process caused the user's historical slowdown. Those services were left untouched.

The reproducible new browser fixture is `frontend/tests/browser/table.html`; build from frontend with `vite build --config vite.table-test.config.js`. It generates pages on demand without allocating a million-row collection. Output is under ignored `.runtime/frappe-table-harness`. Six Core frontend unit tests cover page bounds, stale requests, fuzzy search and reverse boundaries. Real Frappe/provider end-to-end workload measurements remain pending.
