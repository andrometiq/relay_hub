# Hub UI — first administration views

Implemented with actual Frappe UI controls: Overview, Accounts list/new/edit, Channels and explicit not-connected states for Delivery and Events. Account drafts validate name, HTTPS site origin and selected channels. Account drafts survive view changes but reset on page reload. This does not provision a site or save accounts to PostgreSQL. Infrastructure readiness remains live.

`DocumentForm` provides a common contract for Data, Select, Check, Link, MultiSelect and Small Text with required/field errors and responsive columns. Current Link options are provided by the caller; remote permission-filtered lookup, complete DocType layout/child-table support and server save validation remain to implement. The matching component is currently copied into Core; a versioned shared package is deferred until the contract stabilizes.

Browser verification: required fields, invalid site URL, multi-channel selection, saved draft shown in channel view, account search and 390px layout without page overflow. Screenshots use a synthetic example.com site and do not represent a live connection.
