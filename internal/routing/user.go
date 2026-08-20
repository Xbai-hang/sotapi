package routing

// User is a human capable of answering completion requests through a Channel.
// Recipient is an opaque Channel-specific address, such as a Telegram chat ID.
type User struct {
	ID        string
	Channel   string
	Recipient string
}

// Target is the resolved human destination for a model request.
type Target struct {
	User User
}
