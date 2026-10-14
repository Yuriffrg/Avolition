// Collapse duplicate rows for display only. Keep the user's source paths/labels intact.
export function visibleClips(clips, {query = '', hideDuplicates = true, selectedPath = '', entityPath = ''} = {}) {
  const search = query.trim().toLocaleLowerCase();
  const matching = clips.filter(clip => !search || [clip.name, clip.path, clip.annotation?.name]
    .some(value => String(value || '').toLocaleLowerCase().includes(search)));
  if (!hideDuplicates) return matching;
  const groups = new Map();
  const commonPrefix = path => {
    const a = String(path).toLowerCase(), b = String(entityPath).toLowerCase();
    let i = 0; while (i < Math.min(a.length, b.length) && a[i] === b[i]) i++;
    return i;
  };
  const preferred = (a, b) => (Number(a.path === selectedPath) - Number(b.path === selectedPath))
    || (Number(Boolean(a.annotation?.name?.trim())) - Number(Boolean(b.annotation?.name?.trim())))
    || (commonPrefix(a.path) - commonPrefix(b.path))
    || String(b.path).localeCompare(String(a.path));
  for (const clip of matching) {
    const key = clip.error || clip.duplicateError ? clip.path : clip.duplicateKey || clip.path;
    const previous = groups.get(key);
    if (!previous || preferred(clip, previous) > 0) groups.set(key, clip);
  }
  return [...groups.values()];
}
