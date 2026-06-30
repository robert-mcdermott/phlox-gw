# Frontend Implementation Plan

Chunked implementation tasks derived from [FRONTEND_UPGRADE.md](./FRONTEND_UPGRADE.md).
Each chunk is scoped to stay under 100k tokens.

---

## Chunk 1 — Scaffold & Config
**Phase 0 complete** · Est. ~20–30k tokens

- [ ] Run `npm create vite@latest` with `react-ts` template inside `frontend/`
- [ ] Install Tailwind v4 + `@tailwindcss/vite`
- [ ] Run `npx shadcn@latest init`
- [ ] Configure `vite.config.ts` (`outDir: 'dist'`, `/api` proxy pointing to Go server)
- [ ] Update `package.json` build script to `vite build`
- [ ] Verify `go build` still works with new `dist/`

> Small, mechanical setup — no reading of `app.js` yet.

---

## Chunk 2 — Types & API Layer
**Phase 1 complete** · Est. ~40–60k tokens

- [ ] Read through `app.js` in full to extract all state shapes and `fetch()` calls
- [ ] Write `src/types/index.ts` — interfaces for `Model`, `Provider`, `ApiKey`, `User`, `Budget`, `RateLimit`, `AuditLog`, `UsageStats`, `RequestLog`, `GuardrailPolicy`, `OidcConfig`, etc.
- [ ] Write `src/lib/api.ts` — all typed async API functions
- [ ] Install + configure Zustand, write `src/store/index.ts`
- [ ] Write `src/lib/theme.ts` — theme switching + `localStorage` persistence

> Reading the 1,800-line `app.js` is the main token cost here.

---

## Chunk 3 — Auth + Layout Shell
**Phase 2, part 1** · Est. ~30–40k tokens

- [ ] `src/components/AuthScreen.tsx` — login form
- [ ] `src/components/Layout.tsx` — top-level layout wrapper
- [ ] `src/components/Sidebar.tsx` — tab navigation + theme picker swatches
- [ ] `src/components/TopBar.tsx` — user info, logout
- [ ] Wire up theme switcher to store
- [ ] Shadcn components needed: `Button`, `Card`, `DropdownMenu`
- [ ] Verify app renders correctly in logged-in and logged-out states

> Focused scope — no data pages yet.

---

## Chunk 4 — User-Facing Pages
**Phase 2, part 2** · Est. ~50–70k tokens

- [ ] `src/pages/OverviewPage.tsx` — health status + usage summaries
- [ ] `src/pages/ModelsPage.tsx` — model list
- [ ] `src/pages/ApiKeysPage.tsx` — key management (create/revoke)
- [ ] `src/pages/UsagePage.tsx` — placeholder wired to store (charts added in Chunk 7)
- [ ] Shadcn components needed: `Table`, `Badge`, `Dialog`, `Input`, `Select`

---

## Chunk 5 — Admin Pages
**Phase 2, part 3** · Est. ~60–80k tokens

- [ ] `src/pages/admin/OperationsTab.tsx`
- [ ] `src/pages/admin/UsersTab.tsx`
- [ ] `src/pages/admin/BudgetsTab.tsx`
- [ ] `src/pages/admin/RateLimitsTab.tsx`
- [ ] `src/pages/admin/AdminKeysTab.tsx`
- [ ] `src/pages/admin/AdminModelsTab.tsx`
- [ ] `src/pages/admin/AuditLogsTab.tsx`
- [ ] `src/pages/admin/RequestLogTab.tsx` — paginated + filterable
- [ ] `src/pages/admin/GuardrailTab.tsx` — policy editor + preview panel

> Largest chunk due to number of tabs. If token budget runs tight, split into:
> - **5a**: OperationsTab, UsersTab, BudgetsTab, RateLimitsTab
> - **5b**: AdminKeysTab, AdminModelsTab, AuditLogsTab, RequestLogTab, GuardrailTab

---

## Chunk 6 — Theming
**Phase 3 complete** · Est. ~20–30k tokens

- [ ] Create `src/styles/themes.css` with one `[data-theme="..."]` block per theme
- [ ] Map all 8 existing themes to Shadcn CSS variable names (`--background`, `--foreground`, `--primary`, etc.)
- [ ] Test each theme visually

| Theme ID | Name | Dark? |
|---|---|---|
| `phlox-dark` | Phlox Dark | ✅ |
| `phlox-light` | Phlox Light | ❌ |
| `fred-hutch` | Fred Hutch | ❌ |
| `light` | Light | ❌ |
| `dark` | Dark | ✅ |
| `hutch-night` | Hutch Night | ✅ |
| `sandstone` | Sandstone | ❌ |
| `terminal` | Terminal | ✅ |

---

## Chunk 7 — Charts & Data Viz
**Phase 4 complete** · Est. ~30–40k tokens

- [ ] Install Recharts
- [ ] `src/components/charts/UsageSeriesChart.tsx` — time-series usage
- [ ] `src/components/charts/DrilldownChart.tsx` — per-provider and per-model breakdowns
- [ ] `src/components/charts/BudgetBurnDown.tsx` — budget burn-down
- [ ] Wire charts into `UsagePage.tsx` and `OperationsTab.tsx`

---

## Chunk 8 — Cleanup & Verification
**Phase 5 complete** · Est. ~20–30k tokens

- [ ] Delete `frontend/src/static/` (old vanilla files)
- [ ] Delete `frontend/build.mjs`
- [ ] Update `frontend/index.html` to Vite standard template with `<script type="module" src="/src/main.tsx">`
- [ ] Move `phlox-logo.svg` to `src/assets/` and update imports
- [ ] Add `node_modules/` and `dist/` to `frontend/.gitignore`
- [ ] Run `/verify` to confirm full app works end-to-end
- [ ] Update `FRONTEND_UPGRADE.md` and root `README` with new dev workflow

---

## Summary

| Chunk | Phase | Est. Tokens |
|---|---|---|
| 1 — Scaffold & Config | Phase 0 | ~20–30k |
| 2 — Types & API Layer | Phase 1 | ~40–60k |
| 3 — Auth + Layout Shell | Phase 2a | ~30–40k |
| 4 — User-Facing Pages | Phase 2b | ~50–70k |
| 5 — Admin Pages | Phase 2c | ~60–80k |
| 6 — Theming | Phase 3 | ~20–30k |
| 7 — Charts & Data Viz | Phase 4 | ~30–40k |
| 8 — Cleanup & Verification | Phase 5 | ~20–30k |
