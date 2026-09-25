package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// dataFile is a changed non-source file (testdata, fixtures, generators under
// testdata) with the directory of the package that contains it.
type dataFile struct {
	path       string // absolute
	repoPath   string // repository relative, slash separated
	packageDir string // absolute directory of the owning package
}

// pathReference is one path a Go file names through string literals: a
// literal on its own, or the literal arguments of a filepath/path.Join call.
type pathReference struct {
	file string // absolute Go file path
	path string // the named path, slash separated, uncleaned
}

// foreignDataReference returns a full-scope reason when any Go file outside
// a changed data file's own package directory names a path that contains the
// file (directly, relative to the Go file, relative to the repository or
// module root, or as a multi-segment suffix such as
// "transport/cli/testdata"). Tests of such a package read the data without
// linking its owner, so the reverse dependency closure cannot see them.
func foreignDataReference(repoRoot string, moduleDirs []string, files []dataFile) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	references, unparsable, err := collectPathReferences(moduleDirs)
	if err != nil {
		return "", err
	}
	if unparsable != "" {
		// Its path literals are unknown, so no data file is provably private.
		return "changed data files, and " + repoRelative(repoRoot, unparsable) + " cannot be parsed for data references", nil
	}
	for _, file := range files {
		for _, reference := range references {
			if filepath.Dir(reference.file) == file.packageDir {
				continue
			}
			if referencesDataFile(reference, file, repoRoot, moduleDirs) {
				return "changed " + file.repoPath + " is referenced from " + repoRelative(repoRoot, reference.file), nil
			}
		}
	}
	return "", nil
}

func referencesDataFile(reference pathReference, file dataFile, repoRoot string, moduleDirs []string) bool {
	if !hasRealSegment(reference.path) || strings.ContainsAny(reference.path, "\n%*?{}") {
		return false
	}
	named := filepath.FromSlash(reference.path)
	fileRelative := []string{filepath.Join(filepath.Dir(reference.file), named)}
	var rootRelative []string
	// A root-relative reading needs two real segments: a lone "agent-cli"
	// or "internal" names a module or a huge tree, not a fixture.
	if realSegments(reference.path) >= 2 {
		rootRelative = append(rootRelative, filepath.Join(repoRoot, named))
		for _, moduleDir := range moduleDirs {
			rootRelative = append(rootRelative, filepath.Join(moduleDir, named))
		}
	}
	for _, candidate := range append(fileRelative, rootRelative...) {
		candidate = filepath.Clean(candidate)
		// A path that contains the referencing file itself (".", "x/..",
		// an ancestor) names the reader's own tree, not another's data.
		if pathWithin(candidate, reference.file) || isRootOrAncestor(candidate, repoRoot, moduleDirs) {
			continue
		}
		if pathWithin(candidate, file.path) {
			return true
		}
	}
	return suffixReference(reference.path, file.repoPath)
}

func isRootOrAncestor(candidate, repoRoot string, moduleDirs []string) bool {
	for _, root := range append([]string{repoRoot}, moduleDirs...) {
		if pathWithin(candidate, root) {
			return true
		}
	}
	return false
}

func realSegments(path string) int {
	count := 0
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment != "" && segment != "." && segment != ".." {
			count++
		}
	}
	return count
}

// hasRealSegment reports whether a path names something beyond "." and "..",
// so a bare "." or "../.." never claims a whole directory tree.
func hasRealSegment(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment != "" && segment != "." && segment != ".." {
			return true
		}
	}
	return false
}

// suffixReference matches a multi-segment path (after dropping leading "./"
// and "../" segments) that appears on segment boundaries in repoPath.
func suffixReference(named, repoPath string) bool {
	segments := strings.Split(filepath.ToSlash(named), "/")
	for len(segments) > 0 && (segments[0] == "." || segments[0] == ".." || segments[0] == "") {
		segments = segments[1:]
	}
	for len(segments) > 0 && segments[len(segments)-1] == "" {
		segments = segments[:len(segments)-1]
	}
	if len(segments) < 2 {
		return false
	}
	needle := strings.Join(segments, "/")
	haystack := "/" + repoPath + "/"
	return strings.Contains(haystack, "/"+needle+"/")
}

// collectPathReferences parses every Go file under the module directories
// (testdata generators included) and returns the paths their string
// literals and literal Join arguments name, plus the first file that does not
// parse.
func collectPathReferences(moduleDirs []string) ([]pathReference, string, error) {
	var goFiles []string
	for _, moduleDir := range moduleDirs {
		found, err := listGoFiles(moduleDir)
		if err != nil {
			return nil, "", err
		}
		goFiles = append(goFiles, found...)
	}
	var references []pathReference
	fileSet := token.NewFileSet()
	for _, path := range goFiles {
		found, parsed := fileReferences(fileSet, path)
		if !parsed {
			return nil, path, nil
		}
		references = append(references, found...)
	}
	return references, "", nil
}

// listGoFiles returns every .go file below moduleDir, skipping hidden and
// vendor directories.
func listGoFiles(moduleDir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(moduleDir, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.IsDir() && path != moduleDir && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor"):
			return filepath.SkipDir
		case !entry.IsDir() && strings.HasSuffix(path, ".go"):
			files = append(files, canonicalPath(path))
		}
		return nil
	})
	return files, err
}

// fileReferences returns the paths one Go file names, and false when the
// file cannot be read or parsed.
func fileReferences(fileSet *token.FileSet, path string) ([]pathReference, bool) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	parsed, err := parser.ParseFile(fileSet, path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	var references []pathReference
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.BasicLit:
			if value, ok := stringLiteral(typed); ok && value != "" {
				references = append(references, pathReference{file: path, path: value})
			}
		case *ast.CallExpr:
			if joined, ok := literalJoin(typed); ok {
				references = append(references, pathReference{file: path, path: joined})
			}
		}
		return true
	})
	return references, true
}

func stringLiteral(literal *ast.BasicLit) (string, bool) {
	if literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

// literalJoin joins the string literal arguments of a Join call in order,
// skipping non-literal arguments (typically a computed base directory).
func literalJoin(call *ast.CallExpr) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Join" {
		return "", false
	}
	var parts []string
	for _, argument := range call.Args {
		if literal, ok := argument.(*ast.BasicLit); ok {
			if value, ok := stringLiteral(literal); ok {
				parts = append(parts, value)
			}
		}
	}
	if len(parts) < 2 {
		return "", false
	}
	return strings.Join(parts, "/"), true
}

// changedPackagesFor maps every changed path inside a module to the package
// owning its nearest enclosing package directory. Changed coverage floors
// map to the package they describe. A file inside a module but outside
// every package (other than documentation) cannot be scoped, and neither can
// a data file (anything but a package's own Go source) that a Go file of
// another package names by path: its readers do not link its owner.
func changedPackagesFor(paths []string, repoRoot string, modules []AffectedModule, packages map[string]*affectedPackage) (map[string]struct{}, string, error) {
	byDirectory := make(map[string]string, len(packages))
	for _, packageInfo := range packages {
		byDirectory[packageInfo.directory] = packageInfo.importPath
	}
	moduleDirs := absoluteModuleDirs(modules)
	changed := make(map[string]struct{})
	var data []dataFile
	for _, path := range paths {
		manifest := strings.HasPrefix(path, coverageManifestDirectory)
		absolute := filepath.Join(repoRoot, filepath.FromSlash(changeTarget(path, manifest)))
		moduleDir := enclosingModule(absolute, moduleDirs)
		if moduleDir == "" {
			continue
		}
		importPath := enclosingPackage(filepath.Dir(absolute), moduleDir, byDirectory)
		if importPath == "" && (manifest || strings.HasSuffix(path, ".md")) {
			continue
		}
		if importPath == "" {
			return nil, "changed " + path + " outside every package", nil
		}
		changed[importPath] = struct{}{}
		if !manifest && !isOwnSource(path, absolute, byDirectory) {
			data = append(data, dataFile{path: absolute, repoPath: path, packageDir: packages[importPath].directory})
		}
	}
	reason, err := foreignDataReference(repoRoot, moduleDirs, data)
	if err != nil || reason != "" {
		return nil, reason, err
	}
	return changed, "", nil
}

// changeTarget maps a coverage-manifest fragment to a path inside the package
// directory it describes; any other path maps to itself.
func changeTarget(path string, manifest bool) string {
	if !manifest {
		return path
	}
	return strings.TrimSuffix(strings.TrimPrefix(path, coverageManifestDirectory), ".json") + "/floor"
}

func absoluteModuleDirs(modules []AffectedModule) []string {
	moduleDirs := make([]string, 0, len(modules))
	for _, module := range modules {
		if absolute, err := filepath.Abs(module.Directory); err == nil {
			moduleDirs = append(moduleDirs, canonicalPath(absolute))
		}
	}
	return moduleDirs
}

// isOwnSource reports whether a changed path is a Go source file of the
// package in its own directory (not a generator under testdata).
func isOwnSource(path, absolute string, byDirectory map[string]string) bool {
	_, ownDirectory := byDirectory[filepath.Dir(absolute)]
	return isGoFile(path) && ownDirectory
}

func enclosingModule(path string, moduleDirs []string) string {
	best := ""
	for _, moduleDir := range moduleDirs {
		if pathWithin(moduleDir, path) && len(moduleDir) > len(best) {
			best = moduleDir
		}
	}
	return best
}

func enclosingPackage(directory, moduleDir string, byDirectory map[string]string) string {
	for pathWithin(moduleDir, directory) {
		if importPath, ok := byDirectory[directory]; ok {
			return importPath
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return ""
}
