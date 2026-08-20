package routing

// Model describes one model name exposed to API clients and the user pool that
// answers requests for it.
type Model struct {
	ID     string
	PoolID string
}
