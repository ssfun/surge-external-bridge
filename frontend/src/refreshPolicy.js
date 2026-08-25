export function shouldRefreshInBackground(documentHidden, paused) {
  return !documentHidden && !paused
}
