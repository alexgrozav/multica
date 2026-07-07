// repoLabel derives a short display name from a repo URL ("multica" from
// "https://github.com/multica-ai/multica.git"). Same derivation as the scripts
// list and the log tabs use locally.
export function repoLabel(url: string): string {
  let u = url.trim().replace(/\/$/, "").replace(/\.git$/, "");
  const i = Math.max(u.lastIndexOf("/"), u.lastIndexOf(":"));
  if (i >= 0) u = u.slice(i + 1);
  return u || url;
}
