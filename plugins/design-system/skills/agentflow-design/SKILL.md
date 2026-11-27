---
name: agentflow-design
description: Build a React interface with the Agentflow design system - its tokens, components and page templates. Use when asked to build, restyle or review UI that should look like Agentflow, or to add the @agentflow/design-* packages to a project.
---

# Build with the Agentflow design system

Use this skill when a React interface should look like Agentflow: a console, dashboard, docs site or prototype. The packages need React 18.3 or 19 and a bundler that imports CSS from JavaScript. For anything else, only the tokens apply.

Everything below comes from the three packages this plugin carries. Before you use a component, read its `.d.ts` in `reference/`. Do not guess a prop, a class or a token.

## Find the files

Find the absolute path of this installed `SKILL.md`. Its directory is `<plugin-root>/skills/agentflow-design`.

| Path | What it is |
| --- | --- |
| `<plugin-root>/vendor/` | The three npm tarballs |
| `reference/tokens/tokens.css` | Every token, with comments |
| `reference/components/dist/base.css` | Reset, type ramp, layout helpers, motion |
| `reference/components/dist/{atoms,molecules,organisms}/*.d.ts` | Props of every component |
| `reference/patterns/dist/templates/*.d.ts` | Props of the page templates |
| `reference/patterns/dist/styles.css` | Template regions and scroll rules |

The `reference/` files are exact copies of the files in the tarballs.

## Install into the target project

The packages are not on a registry. Never install `@agentflow/design-*` by name: whatever a registry serves under those names is not this design system. Copy the tarballs into the project and install them by path, all in one command:

```sh
mkdir -p vendor
cp "<plugin-root>"/vendor/*.tgz vendor/
npm install \
  ./vendor/agentflow-design-tokens-0.1.0.tgz \
  ./vendor/agentflow-design-components-0.1.1.tgz \
  ./vendor/agentflow-design-patterns-0.1.1.tgz
```

Install all three: components needs tokens and patterns needs components, and a package manager does not fetch a missing one for you. The same paths work with `pnpm add`; with `yarn add`, prefix each with `file:`. Commit `vendor/` so the `file:` dependencies resolve for everyone.

Import the styles once, in this order, at the app entry:

```js
import "@agentflow/design-tokens/tokens.css";
import "@agentflow/design-components/styles.css";
import "@agentflow/design-patterns/styles.css";
```

`styles.css` of the components is `base.css`, `atoms.css`, `molecules.css` and `organisms.css` joined; each is also exported on its own.

The packages ship no font files. Load DM Sans (400 to 700) and DM Mono (400, 500) yourself, or the `--font` and `--mono` fallbacks apply.

## Scope and theme

Every rule is scoped to the class `agentflow-ui`. Nothing is styled outside it, and no token exists outside it.

```jsx
<div className="agentflow-ui" data-theme="light">…</div>
```

- Dark is the default. `data-theme="light"` on the scope element, or on an ancestor when the scope element has no `data-theme`, switches to light. `data-theme="dark"` forces dark inside a light ancestor.
- The CSS does not read `prefers-color-scheme`. Set `data-theme` yourself.
- The scope element takes `--bg`, `--text` and the body type. Give it the height the layout needs: the shell and page templates are `height: 100%`.

## Imports

```js
import { Button, Row, Stack, Text } from "@agentflow/design-components/atoms";
import { Panel, StatTile, Toolbar } from "@agentflow/design-components/molecules";
import { DataTable, PageHeader } from "@agentflow/design-components/organisms";
import { DashboardTemplate, ShellTemplate } from "@agentflow/design-patterns";
```

`@agentflow/design-components` exports all three levels. `@agentflow/design-patterns/templates` is the same as the package root.

## Tokens

Use tokens for every colour, size and space. Exact values are in `reference/tokens/tokens.css`.

| Group | Tokens | Use |
| --- | --- | --- |
| Ground | `--bg`, `--surface`, `--surface-2` | Page, panel, raised or hover fill |
| Text | `--text`, `--text-muted`, `--text-subtle` | Body, secondary, captions |
| Lines | `--border`, `--border-strong`, `--keyline`, `--grid` | Keylines (`--keyline` is the width), chart grid |
| Accent | `--accent`, `--blue-wash`, `--on-blue`, `--focus`, `--primary-hover` | Links and marks, selection wash, text on blue, focus ring |
| Status | `--ok`, `--warn`, `--bad`, `--info`, each with `-bg` | State, always beside a label |
| Charts | `--series-1` to `--series-8` | Series colours; none repeats `--ok`, `--warn` or `--bad` |
| Brand | `--brand-midnight`, `--brand-blue`, `--brand-blue-deep`, `--brand-chalk`, `--brand-frost`, `--brand-slate`, `--brand-spearmint` | Same in both themes. Spearmint is accent only: a live signal, one highlight, primary hover |
| On dark | `--on-dark-primary`, `--on-dark-secondary`, `--on-dark-faint` | Text on a surface that stays dark, such as the rail |
| Space | `--s-1` to `--s-10` | 4, 8, 12, 16, 24, 32, 48, 64, 96, 128px |
| Type | `--font`, `--mono`, `--fs-2xs` to `--fs-6xl` | 11px to 32px, in rem; body is `--fs-md` |
| Motion | `--t-fast`, `--t-med`, `--t-slow`, `--ease-out`, `--ease-in-out` | 120, 240, 520ms |
| Layout | `--rail-w`, `--rail-w-collapsed`, `--topbar-h`, `--statusbar-h`, `--row-h` | Shell regions and row height |
| Shape | `--radius` | `0` |

## Components

Atoms (`/atoms`):

- Layout: `Box`, `Row`, `Stack`, `Spacer`, `Grid`, `Split`
- Text: `Text`, `Mono`, `Eyebrow`, `Kbd`, `Tag`, `Link`
- Actions and fields: `Button`, `Clickable`, `Chip`, `Input`, `Textarea`, `Select`, `Checkbox`, `Switch`, `DayInput`
- Status and data: `Pill`, `StatusDot`, `Meter`, `Spark`, `Swatch`, `AnimatedNumber`, `Skeleton`
- Brand and chrome: `Logo`, `Icon`, `Avatar`, `Corners`, `Node`, `Scrim`
- Also: `LinkAdapterProvider`, `useCountUp`, `useHotkey`, `useReducedMotion`, type `Tone`

Molecules (`/molecules`):

- Surfaces: `Panel`, `Banner`, `EmptyState`, `LoadError`, `Toast`, `Popover`
- Headers: `PageTitle`, `DrawerHeader`, `ModalHeader`, `Breadcrumb`
- Toolbars and filters: `Toolbar`, `ToolbarGroup`, `ToolbarSep`, `ChipScroll`, `SearchBox`, `Segmented`, `Tabs`, `ColumnsMenu`, `Pagination`, `PageSizeSelect`, `LiveToggle`
- Forms: `FormField`, `FormGrid`
- Data: `StatTile`, `KeyValue`, `JsonView`, `CopyText`, `Legend`, `ChartTip`, `EnabledPill`, `Stepper`, `Timeline`, `TimelineItem`, `FlowNodeCard`
- Navigation and lists: `NavGroup`, `NavItem`, `StatusItem`, `ListRow`, `LinkRow`, `PaletteInput`, `PaletteRow`

Organisms (`/organisms`):

- `PageHeader`, `DataTable`, `Drawer`, `RecordDrawer`, `Modal`, `ConfirmDialog`, `Toaster`, `SchemaForm`, `ErrorBoundary`
- Charts: `LineChart`, `BarChart`, `Donut`, `HeatStrip`, `HeatGrid`, `FlowDiagram`
- Also: `useSize`, types `Column`, `DataTableProps`, `LineSeries`, `BarKey`, `FlowColumn`, `FlowLink`, `FlowNode`, `ToastMessage`, `JsonSchema`

Props you will use most. Read the `.d.ts` for the rest.

| Component | Props |
| --- | --- |
| `Button` | `variant`: `primary`, `secondary` (default), `ghost`, `danger`, `link`. `size`: `sm`, `md`. `icon`, `iconRight`, `loading`, `to`, plus button attributes |
| `Text` | `as`, `variant` (`h1` to `h4`, `body`, `small`, `eyebrow`, `caption`, `label`, `mono`, `mono-lg`, `mono-xl`), `tone` (`muted`, `subtle`, `ok`, `warn`, `bad`, `info`, `accent`), `weight`, `truncate`, `nowrap`, `clamp`, `grow`, `srOnly` |
| `Row`, `Stack` | `as`, `gap` (0 to 6, default 8px), `align`, `justify`, `wrap`, `grow` |
| `Grid` | `cols`: 2, 3, 4 or 6 |
| `Pill` | `tone` (`ok`, `warn`, `bad`, `info`, `neutral`), `dot`, `pulse`, `solid`, `title`, `to` |
| `Icon` | `name` (the names are listed in `atoms/Icon.d.ts`), `size` |
| `Logo` | `variant`: `mark` (default) or `full`. `height` |
| `Panel` | `title`, `eyebrow`, `meta`, `actions`, `footer`, `flush`, `fill`, `stacked`, `corners` |
| `StatTile` | `label`, `value` or `text`, `format`, `unit`, `delta`, `deltaTone`, `spark`, `tone`, `to`, `hint`, `size` |
| `FormField` | `label`, `hint`, `error`, `required`, `span`; the control is the child |
| `PageHeader` | `title`, `description`, `eyebrow`, `actions`, `meta` |
| `DataTable` | `rows`, `columns`, `rowKey`, `onRowClick`, `selectedKey`, `defaultSort`, `rowTone`, `flashKeys`, `loading`, `empty`, `maxHeight`, `animate`, `density` |
| `Drawer`, `Modal` | `open`, `onClose`, `title`, `footer`, `width`; `Drawer` also takes `eyebrow` and `actions` |

### Links need an adapter for client-side routing

`Link`, and the `to` prop of `Button`, `Pill`, `StatTile`, `ListRow`, `NavItem` and `Breadcrumb`, render a plain `<a href>` unless you supply a router adapter. Wrap the app in `LinkAdapterProvider` with a component that takes `LinkAdapterProps` (`to`, `nav`, `end`, `activeClassName`) and renders your router's link.

### Two components render classes this CSS does not style

`Split` renders `pg-split`, `pg-halves` or `pg-thirds`, and `ListRow` renders `pg-list-row`. The shipped stylesheets have no rules for those classes. Write the rules in the project, or use `Grid` and `Row`.

## Page templates

From `@agentflow/design-patterns`. A page picks one template and fills its slots; the template owns the regions and the scrolling.

| Template | Slots | Use |
| --- | --- | --- |
| `ShellTemplate` | `rail`, `top`, `status`, `overlays`, `routeKey`, `children` | App frame around every page |
| `DashboardTemplate` | `header`, `kpis`, `children` | Header, KPI strip, scrolling stack of panels |
| `ListTemplate` | `header`, `kpis`, `above`, `toolbar`, `children`, `footer`, `drawer` | Full-height table with toolbar and drawer |
| `DetailTemplate` | `header`, `kpis`, `tabs`, `children`, `aside`, `drawer`, `asideWidth` (340), `asideFlush` | Main column with an aside |
| `WorkbenchTemplate` | `header`, `kpis`, `toolbar`, `left`, `children`, `right`, `leftWidth` (300), `rightWidth` (360) | List, canvas and inspector side by side |
| `CanvasTemplate` | `header`, `toolbar`, `children`, `hud`, `side` | Full-bleed stage with overlays |

- Pass a `PageHeader` as `header` and `StatTile`s as `kpis`.
- `ShellTemplate` only lays out a grid with the areas `rail`, `top`, `main` and `status`. The CSS places the page region and an element with the class `rail`. You write the rail, top bar and status bar containers and put the top bar and status bar in their areas. `NavGroup`, `NavItem` and `StatusItem` are styled for use inside them.

## Rules the CSS encodes

Follow these so your own markup matches the components.

- **Square and flat.** `--radius` is `0`. Surfaces are divided by `--keyline` borders in `--border`. The stylesheets use `box-shadow` only for inset keylines, focus and the pulse on a live marker. Add no rounded corners and no drop shadows.
- **Tokens only.** No literal colours. Font sizes come from `--fs-*`; never set one in px, and nothing goes below 11px.
- **Spacing on the 4px grid.** Use `--s-*`, or the `gap` prop of `Row` and `Stack`.
- **DM Sans for words, DM Mono for numbers and specs.** Mono is for numbers, keys, addresses, versions and table values, with tabular numerals. Headings `h1` and `h2` are bold with -3% tracking. Eyebrows are regular weight and sentence case, never tracked capitals.
- **Status is never colour alone.** A `Pill`, `StatusDot` or status-coloured `Text` sits beside a label. Use `Pill` for state and `Tag` for a spec.
- **One primary button per view.** `primary` is brand blue and wipes to spearmint on hover. Everything else is `secondary`, `ghost` or `danger`.
- **Content sits in panels.** Use `Panel` for each block, `flush` for tables and charts that run edge to edge, and `Grid` for a keylined row of cells.
- **One breakpoint, 1100px.** At or below it, a `Grid` of four or six columns drops to two and a grid of three drops to one, the KPI strip wraps to three columns and the `DetailTemplate` aside is hidden. Add no other breakpoints inside the templates.
- **Motion is built in.** Page headers, the KPI strip, charts, drawers, modals and toasts animate on mount, and the CSS turns animation off under `prefers-reduced-motion`. For a staggered entrance use the `stagger` or `rise` class from `base.css`; write no new keyframes for it.
- **Focus stays visible.** `:focus-visible` draws a 2px `--focus` outline; the data table, search and palette inputs show it as an inset `--focus` edge instead. Do not remove either.

## Before you finish

1. Every component and prop you used exists in the `.d.ts` files.
2. The root element has `agentflow-ui`, and `data-theme` when the page is light.
3. The three stylesheets are imported once, tokens first.
4. Your own CSS uses tokens, has no border radius and no drop shadow, and is scoped under `.agentflow-ui`.
5. The project builds, and the page reads correctly in both themes.
