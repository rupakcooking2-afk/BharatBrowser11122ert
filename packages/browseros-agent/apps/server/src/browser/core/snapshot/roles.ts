// Role classification driving the snapshot renderer. A node's role decides whether
// it gets a line, a `[ref=eN]` handle, or is dropped (its children rising to its depth).

/** Roles a user can act on — these receive a `[ref=eN]` handle. */
export const INTERACTIVE_ROLES: ReadonlySet<string> = new Set([
  'button',
  'link',
  'textbox',
  'searchbox',
  'textarea',
  'checkbox',
  'radio',
  'combobox',
  'menuitem',
  'menuitemcheckbox',
  'menuitemradio',
  'tab',
  'switch',
  'slider',
  'spinbutton',
  'option',
  'treeitem',
  'listbox',
  'DisclosureTriangle',
])

/** Non-actionable roles worth showing for structure/context (no ref). */
export const NAMED_CONTENT_ROLES: ReadonlySet<string> = new Set([
  'heading',
  'img',
  'cell',
  'columnheader',
  'rowheader',
  'dialog',
  'alertdialog',
])

/** Structureless roles: drop the node, render its children at the parent's depth. */
export const SKIP_ROLES: ReadonlySet<string> = new Set([
  'none',
  'presentation',
  'LineBreak',
  'InlineTextBox',
  'StaticText',
  'text',
])

/** Document roots: dropped so the tree starts at the body's children. */
export const ROOT_ROLES: ReadonlySet<string> = new Set([
  'RootWebArea',
  'WebArea',
])

/** Roles whose current value is appended as `: "value"`. */
export const VALUE_ROLES: ReadonlySet<string> = new Set([
  'textbox',
  'searchbox',
  'textarea',
  'combobox',
  'spinbutton',
])
