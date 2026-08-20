package routing

// Pool groups humans that may answer requests. Phase one deliberately requires
// exactly one user per pool; the slice preserves the future multi-user shape
// without implementing a selection strategy prematurely.
type Pool struct {
	ID      string
	UserIDs []string
}
