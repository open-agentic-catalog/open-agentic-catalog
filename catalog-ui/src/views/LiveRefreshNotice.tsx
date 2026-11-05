/** Proof refresh repeats a captured search; it is not a local cache operation. */
export function LiveRefreshNotice() {
  return (
    <p className="muted">
      Refresh repeats the captured Catalog search, including its filters and
      page. It may disclose criteria to current approved sources or send text to
      the configured embedding provider. This is not a local image-cache
      refresh. Your draft stays here; a changed or expired result window needs a
      new search.
    </p>
  );
}
