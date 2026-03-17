package datapipe

import (
	"errors"
	"net/http"
	"sync"
)

var ErrBodyTooLarge = errors.New("datapipe: request body too large")

// DataPipe defines the interface for data channel abstraction layer.
// It provides a pluggable mechanism to intercept HTTP requests and
// route them through custom transport layers (e.g., NATS message queue).
type DataPipe interface {
	// Name returns the unique identifier name of the DataPipe implementation.
	Name() string

	// Init initializes the DataPipe with the provided configuration.
	// The config map contains key-value pairs for DataPipe-specific settings.
	Init(config map[string]string) error

	// GetTransport returns an http.RoundTripper that will be used
	// as the custom transport for HTTP client. This allows the DataPipe
	// to intercept and handle HTTP requests.
	GetTransport() http.RoundTripper

	// Listen starts listening for incoming requests targeted at the
	// specified base URL. This is typically used for setting up
	// message queue subscribers or similar listeners.
	Listen(targetBaseURL string) error
}

// Registry manages DataPipe plugin registrations.
// It provides thread-safe operations for registering and retrieving DataPipe implementations.
type Registry struct {
	mu    sync.RWMutex
	pipes map[string]DataPipe
}

// globalRegistry is the default registry instance.
var globalRegistry = NewRegistry()

// NewRegistry creates a new DataPipe registry instance.
func NewRegistry() *Registry {
	return &Registry{pipes: make(map[string]DataPipe)}
}

// Register adds a DataPipe to the registry.
// If a DataPipe with the same name already exists, it will be overwritten.
func (r *Registry) Register(pipe DataPipe) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pipes[pipe.Name()] = pipe
}

// Get retrieves a DataPipe by name from the registry.
// Returns nil if no DataPipe with the given name is found.
func (r *Registry) Get(name string) DataPipe {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pipes[name]
}

// List returns all registered DataPipe names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.pipes))
	for name := range r.pipes {
		names = append(names, name)
	}
	return names
}

// Register adds a DataPipe to the global registry.
func Register(pipe DataPipe) {
	globalRegistry.Register(pipe)
}

// Get retrieves a DataPipe by name from the global registry.
func Get(name string) DataPipe {
	return globalRegistry.Get(name)
}

// List returns all registered DataPipe names from the global registry.
func List() []string {
	return globalRegistry.List()
}
