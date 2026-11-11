/** Encode a cell as CSV, neutralizing spreadsheet formulas before quoting. */
export function csvField(value: string | number): string {
  let text = String(value);
  if (/^\s*[=+\-@]/.test(text)) text = "'" + text;
  return /[",\r\n]/.test(text) ? '"' + text.replaceAll('"', '""') + '"' : text;
}
