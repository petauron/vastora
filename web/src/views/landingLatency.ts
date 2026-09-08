export function landingLatencyColor(milliseconds: number | null | undefined): string {
  if (milliseconds == null || !Number.isFinite(milliseconds) || milliseconds <= 0) return "text-muted-foreground";
  if (milliseconds < 20) return "text-latency-fast";
  if (milliseconds <= 100) return "text-latency-medium";
  return "text-destructive";
}
