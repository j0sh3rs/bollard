package docker

import (
	"context"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
)

// Event is a simplified Docker container lifecycle event.
type Event struct {
	Type        string // "start" or "stop"
	ContainerID string
	Labels      map[string]string
}

// Watcher subscribes to Docker container events.
type Watcher struct {
	client *dockerclient.Client
}

// NewWatcher creates a Watcher connected to the default Docker socket.
func NewWatcher() (*Watcher, error) {
	c, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	return &Watcher{client: c}, nil
}

// Watch returns a channel of Events and a channel of errors.
// Closes when ctx is cancelled.
func (w *Watcher) Watch(ctx context.Context) (<-chan Event, <-chan error) {
	eventCh := make(chan Event, 64)
	errCh := make(chan error, 1)

	f := filters.NewArgs()
	f.Add("type", "container")
	f.Add("event", "start")
	f.Add("event", "die")
	f.Add("event", "destroy")

	msgCh, dockerErrCh := w.client.Events(ctx, events.ListOptions{Filters: f})

	go func() {
		defer close(eventCh)
		defer close(errCh)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-dockerErrCh:
				if !ok {
					return
				}
				errCh <- err
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				e := Event{ContainerID: msg.Actor.ID, Labels: msg.Actor.Attributes}
				switch msg.Action {
				case "start":
					e.Type = "start"
				case "die", "destroy":
					e.Type = "stop"
				default:
					continue
				}
				select {
				case eventCh <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return eventCh, errCh
}

// Close shuts down the Docker client.
func (w *Watcher) Close() error {
	return w.client.Close()
}
