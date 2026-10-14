package pypath

// ResolveCache memoizes the file-system lookups of Resolve for a short-lived
// batch of resolutions (one asset or map load), where the same folders are
// resolved thousands of times. It assumes the file system does not change
// meanwhile. The zero value is ready to use; it is not safe for concurrent
// use.
type ResolveCache struct {
	links    map[string]linkInfo
	resolved map[string]string
}

// Resolve is Resolve(p) using and filling the cache.
func (c *ResolveCache) Resolve(p string) string {
	if resolved, ok := c.resolved[p]; ok {
		return resolved
	}
	resolved := realpathCached(Absolute(ExpandUser(p)), c)
	if c.resolved == nil {
		c.resolved = map[string]string{}
	}
	c.resolved[p] = resolved
	return resolved
}
