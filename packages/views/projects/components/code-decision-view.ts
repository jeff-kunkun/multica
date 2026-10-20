/** A folder with no Git at all. An empty repository, or one this machine
 *  could not measure, is not this — the first already has a `.git`, the
 *  second must not be told it doesn't. */
export function isPlainFolder(
  isGitRepo: boolean | undefined,
  gitRoot?: string,
): boolean {
  return isGitRepo === false && !gitRoot;
}
