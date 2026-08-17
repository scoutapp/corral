// fmtBytes renders a byte count as a compact human size (KB/MB/GB/TB). Used for
// things like DinD cache sizes, which can run to gigabytes — hence the wider
// unit range than MitmTab's request-body formatter.
export function fmtBytes(n?: number): string {
  if (n == null || n <= 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  // No decimals for bytes; one for everything larger.
  return i === 0 ? `${v} ${units[i]}` : `${v.toFixed(1)} ${units[i]}`;
}
