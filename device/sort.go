package device

import "sort"

// sortKeys gives deterministic iteration order for map-held key sets.
func sortKeys(keys []Key) {
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
}
