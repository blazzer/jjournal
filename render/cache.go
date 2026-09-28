package render

import (
	"os"
	"sort"
	"time"
)

// Evict deletes oldest-mtime files under dirs until the total size is at most maxBytes.
func Evict(dirs []string, maxBytes int64) error {
	type file struct {
		root *os.Root
		name string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	var roots []*os.Root
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return err
		}
		roots = append(roots, root)
		d, err := root.Open(".")
		if err != nil {
			root.Close()
			return err
		}
		infos, err := d.Readdir(-1)
		d.Close()
		if err != nil {
			root.Close()
			return err
		}
		for _, info := range infos {
			if info.IsDir() {
				continue
			}
			files = append(files, file{root: root, name: info.Name(), size: info.Size(), mod: info.ModTime()})
			total += info.Size()
		}
	}
	defer func() {
		for _, r := range roots {
			r.Close()
		}
	}()
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := f.root.Remove(f.name); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= f.size
	}
	return nil
}
