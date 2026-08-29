package webmcp

// NewStatefulBroker is a descriptive constructor alias.
func NewStatefulBroker(options BrokerOptions) *StatefulBroker { return NewBroker(options) }

// New is a concise constructor alias for callers that use the package as a
// broker implementation rather than only as a contract package.
func New(options BrokerOptions) *StatefulBroker { return NewBroker(options) }
