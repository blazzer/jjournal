package render

// Comment is a flat comment before threading.
type Comment struct {
	ID       int64
	ParentID int64
	Author   string
	BodyHTML string
	Deleted  bool
}

// CommentNode is one node in a thread.
type CommentNode struct {
	Comment
	Depth    int
	Children []*CommentNode
}

// CollapseDepth is the depth at which a thread starts collapsed.
const CollapseDepth = 5

// ShouldCollapse reports whether a comment at depth should be wrapped.
func ShouldCollapse(depth int) bool { return depth >= CollapseDepth }

// Thread builds a forest. Cycles and missing parents become roots.
func Thread(items []Comment) []*CommentNode {
	nodes := make([]*CommentNode, len(items))
	byID := make(map[int64]*CommentNode, len(items))
	for i := range items {
		nodes[i] = &CommentNode{Comment: items[i]}
		byID[items[i].ID] = nodes[i]
	}
	var roots []*CommentNode
	for _, n := range nodes {
		if n.ParentID == 0 {
			roots = append(roots, n)
			continue
		}
		p, ok := byID[n.ParentID]
		if !ok || ancestor(byID, p, n.ID) {
			n.ParentID = 0
			roots = append(roots, n)
			continue
		}
		p.Children = append(p.Children, n)
	}
	assign(roots, 1, map[int64]bool{})
	return roots
}

func ancestor(byID map[int64]*CommentNode, start *CommentNode, target int64) bool {
	seen := map[int64]bool{}
	for n := start; n != nil && !seen[n.ID]; n = byID[n.ParentID] {
		if n.ID == target {
			return true
		}
		seen[n.ID] = true
		if n.ParentID == 0 {
			break
		}
	}
	return false
}

func assign(nodes []*CommentNode, depth int, seen map[int64]bool) {
	for _, n := range nodes {
		if seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		n.Depth = depth
		assign(n.Children, depth+1, seen)
	}
}
