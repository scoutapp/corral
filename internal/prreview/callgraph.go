package prreview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// cgNode is a function/method/class definition with its line range.
type cgNode struct {
	filePath  string
	symbol    string
	kind      string // function | method | class
	lineStart int
	lineEnd   int
}

// cgCall is a caller→callee edge by symbol name (resolved to node ids later).
type cgCall struct {
	callerName string
	calleeName string
	filePath   string
}

var supportedExts = map[string]bool{
	".py": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".rb": true, ".go": true,
}

var skipDirs = map[string]bool{
	"node_modules": true, "__pycache__": true, ".git": true,
	"dist": true, "build": true, "venv": true, ".venv": true, "vendor": true,
}

func languageFor(ext string) *sitter.Language {
	switch ext {
	case ".py":
		return python.GetLanguage()
	case ".tsx", ".jsx":
		return tsx.GetLanguage()
	case ".ts":
		return typescript.GetLanguage()
	case ".js":
		return javascript.GetLanguage()
	case ".rb":
		return ruby.GetLanguage()
	case ".go":
		return golang.GetLanguage()
	}
	return nil
}

// BuildCallgraph materializes the repo's default branch to a temp worktree,
// parses every supported source file with tree-sitter to extract function defs
// and call edges, and writes pr_cg_nodes/pr_cg_edges for the repo (replacing
// prior rows). Returns (nodeCount, edgeCount).
//
// gitDir is the repo's bare mirror (Repo.CachePath); defaultBranch is checked
// out via `git archive` into a temp dir we clean up.
func (s *Service) BuildCallgraph(ctx context.Context, repoID, gitDir, defaultBranch string) (int, int, error) {
	dir, err := os.MkdirTemp("", "corral-cg-")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(dir)

	if err := extractTree(gitDir, defaultBranch, dir); err != nil {
		return 0, 0, err
	}

	var nodes []cgNode
	var calls []cgCall
	parser := sitter.NewParser()

	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !supportedExts[ext] {
			return nil
		}
		lang := languageFor(ext)
		if lang == nil {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil || len(src) == 0 {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		parser.SetLanguage(lang)
		tree, err := parser.ParseCtx(ctx, nil, src)
		if err != nil || tree == nil {
			return nil
		}
		fn, cl := extractDefsAndCalls(tree.RootNode(), src, rel)
		nodes = append(nodes, fn...)
		calls = append(calls, cl...)
		return nil
	})
	if walkErr != nil {
		return 0, 0, walkErr
	}

	return s.writeCallgraph(repoID, nodes, calls)
}

// extractTree checks out branch from a bare mirror into destDir via git archive.
func extractTree(gitDir, branch, destDir string) error {
	if branch == "" {
		branch = "HEAD"
	}
	archive := exec.Command("git", "--git-dir", gitDir, "archive", branch)
	untar := exec.Command("tar", "-x", "-C", destDir)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	untar.Stdin = pipe
	if err := untar.Start(); err != nil {
		return err
	}
	if err := archive.Run(); err != nil {
		return err
	}
	return untar.Wait()
}

// symbolStack tracks the current enclosing definition name during the walk.
func extractDefsAndCalls(root *sitter.Node, src []byte, filePath string) ([]cgNode, []cgCall) {
	var defs []cgNode
	var calls []cgCall

	text := func(n *sitter.Node) string {
		if n == nil {
			return ""
		}
		return n.Content(src)
	}

	var walk func(n *sitter.Node, current string)
	walk = func(n *sitter.Node, current string) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "function_definition", "function_declaration",
			"method_definition", "method_declaration",
			"method", "singleton_method", "func_literal", "arrow_function":
			name := text(n.ChildByFieldName("name"))
			if name == "" {
				name = arrowName(n, src)
			}
			if name == "" {
				name = "<anonymous>"
			}
			kind := "function"
			if strings.Contains(n.Type(), "method") {
				kind = "method"
			}
			defs = append(defs, cgNode{
				filePath:  filePath,
				symbol:    name,
				kind:      kind,
				lineStart: int(n.StartPoint().Row) + 1,
				lineEnd:   int(n.EndPoint().Row) + 1,
			})
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i), name)
			}
			return

		case "class_definition", "class":
			if n.ChildCount() > 1 {
				name := text(n.ChildByFieldName("name"))
				if name == "" {
					name = "<anonymous>"
				}
				defs = append(defs, cgNode{
					filePath:  filePath,
					symbol:    name,
					kind:      "class",
					lineStart: int(n.StartPoint().Row) + 1,
					lineEnd:   int(n.EndPoint().Row) + 1,
				})
			}
			// classes don't become the "current function" for call attribution
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i), current)
			}
			return

		case "call", "call_expression":
			fn := n.ChildByFieldName("function")
			if fn == nil {
				fn = n.ChildByFieldName("method")
			}
			callee := calleeName(fn, src)
			if callee != "" && current != "" {
				calls = append(calls, cgCall{callerName: current, calleeName: callee, filePath: filePath})
			}
		}

		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i), current)
		}
	}
	walk(root, "")
	return defs, calls
}

// calleeName resolves the called symbol name from a call's function node,
// handling bare identifiers and obj.method / obj->method / pkg.Func forms.
func calleeName(fn *sitter.Node, src []byte) string {
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier", "field_identifier":
		return fn.Content(src)
	case "attribute", "member_expression", "selector_expression", "call":
		for _, field := range []string{"attribute", "property", "field"} {
			if a := fn.ChildByFieldName(field); a != nil {
				return a.Content(src)
			}
		}
	}
	return ""
}

// arrowName returns the variable name an arrow/func literal is assigned to, if
// it's the RHS of a variable declarator (JS/TS const foo = () => …).
func arrowName(n *sitter.Node, src []byte) string {
	p := n.Parent()
	if p != nil && p.Type() == "variable_declarator" {
		if name := p.ChildByFieldName("name"); name != nil {
			return name.Content(src)
		}
	}
	return ""
}

// writeCallgraph persists nodes + edges for a repo (replacing prior rows) and
// returns their counts. Edges connect a caller node to callee nodes by symbol
// name within the repo (best-effort name resolution, like the reference).
func (s *Service) writeCallgraph(repoID string, nodes []cgNode, calls []cgCall) (int, int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM pr_cg_edges WHERE repo_id = ?`, repoID); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(`DELETE FROM pr_cg_nodes WHERE repo_id = ?`, repoID); err != nil {
		return 0, 0, err
	}

	// Insert nodes, remembering id per (file,symbol) and all ids per symbol name.
	stmt, err := tx.Prepare(`
		INSERT INTO pr_cg_nodes (repo_id, file_path, symbol_name, kind, line_start, line_end)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, 0, err
	}
	type key struct{ file, sym string }
	callerID := map[key]int64{}
	idsBySymbol := map[string][]int64{}
	for _, n := range nodes {
		res, err := stmt.Exec(repoID, n.filePath, n.symbol, n.kind, n.lineStart, n.lineEnd)
		if err != nil {
			stmt.Close()
			return 0, 0, err
		}
		id, _ := res.LastInsertId()
		callerID[key{n.filePath, n.symbol}] = id
		idsBySymbol[n.symbol] = append(idsBySymbol[n.symbol], id)
	}
	stmt.Close()

	// Edges: caller (resolved within its file) → every node whose symbol matches
	// the callee name. Cross-file name resolution is intentionally coarse.
	edgeStmt, err := tx.Prepare(`
		INSERT INTO pr_cg_edges (repo_id, caller_id, callee_id) VALUES (?, ?, ?)`)
	if err != nil {
		return 0, 0, err
	}
	defer edgeStmt.Close()
	edgeCount := 0
	seen := map[[2]int64]bool{}
	for _, c := range calls {
		from, ok := callerID[key{c.filePath, c.callerName}]
		if !ok {
			continue
		}
		for _, to := range idsBySymbol[c.calleeName] {
			if from == to || seen[[2]int64{from, to}] {
				continue
			}
			seen[[2]int64{from, to}] = true
			if _, err := edgeStmt.Exec(repoID, from, to); err != nil {
				return 0, 0, err
			}
			edgeCount++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(nodes), edgeCount, nil
}

// InDegrees returns, for a repo, a map of file_path → max callgraph in-degree of
// any node in that file. Used to upgrade block hotness from churn-only to
// in_degree × churn. A file with no nodes maps to 0.
func (s *Service) InDegrees(repoID string) (map[string]int, error) {
	rows, err := s.db.Query(`
		SELECT n.file_path, COUNT(e.id) AS indeg
		  FROM pr_cg_nodes n
		  LEFT JOIN pr_cg_edges e ON e.callee_id = n.id
		 WHERE n.repo_id = ?
		 GROUP BY n.id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byFile := map[string]int{}
	for rows.Next() {
		var file string
		var indeg int
		if err := rows.Scan(&file, &indeg); err != nil {
			return nil, err
		}
		if indeg > byFile[file] {
			byFile[file] = indeg
		}
	}
	return byFile, rows.Err()
}
