export function observeMediaQuery(media: MediaQueryList, listener: (event: MediaQueryListEvent) => void) {
  if (typeof media.addEventListener === "function") {
    media.addEventListener("change", listener);
    return () => media.removeEventListener("change", listener);
  }
  if (typeof media.addListener === "function") {
    media.addListener(listener);
    return () => media.removeListener(listener);
  }
  return () => undefined;
}
