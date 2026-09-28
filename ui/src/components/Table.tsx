import type { ComponentChildren } from 'preact';

/** One column of a Table. */
export interface Column<T> {
  key: string;
  header: ComponentChildren;
  render: (row: T) => ComponentChildren;
  numeric?: boolean;
}

/** Props of Table. */
export interface TableProps<T> {
  caption: string;
  columns: Column<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  empty?: ComponentChildren;
  rowClass?: (row: T) => string | undefined;
}

/** A data table with a caption for assistive technology and an empty state. */
export function Table<T>({ caption, columns, rows, rowKey, empty, rowClass }: TableProps<T>) {
  if (rows.length === 0) {
    return <p class="empty">{empty ?? 'Nothing to show yet.'}</p>;
  }
  return (
    <div class="table-wrap">
      <table class="table">
        <caption class="sr-only">{caption}</caption>
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.key} scope="col" class={c.numeric ? 'num' : undefined}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={rowKey(row)} class={rowClass?.(row)}>
              {columns.map((c) => (
                <td key={c.key} class={c.numeric ? 'num' : undefined}>
                  {c.render(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
