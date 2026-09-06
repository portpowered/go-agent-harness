package architecturemutableglobal

var Registry = map[string]string{} // want "mutable package variable Registry"

func init() {} // want "package init function requires an explicit architecture exception"

const CompileTime = 1

var _ = CompileTime
