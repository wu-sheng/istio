//  Copyright 2018 Istio Authors
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package mcp

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"github.com/gogo/protobuf/proto"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/runtime/schema"

	mcp "istio.io/api/mcp/v1alpha1"
	"istio.io/istio/galley/pkg/kube/converter/legacy"
	"istio.io/istio/galley/pkg/metadata/kube"
	"istio.io/istio/mixer/pkg/config/store"
	"istio.io/istio/pkg/log"
	"istio.io/istio/pkg/mcp/client"
	"istio.io/istio/pkg/mcp/configz"
	"istio.io/istio/pkg/probe"
)

var scope = log.RegisterScope("mcp", "Mixer MCP client stack", 0)

const (
	mixerNodeID      = ""
	eventChannelSize = 4096
)

// Register registers this module as a StoreBackend.
// Do not use 'init()' for automatic registration; linker will drop
// the whole module because it looks unused.
func Register(builders map[string]store.Builder) {
	builders["mcp"] = func(u *url.URL, gv *schema.GroupVersion) (store.Backend, error) { return newStore(u, nil) }
}

// NewStore creates a new Store instance.
func newStore(u *url.URL, fn updateHookFn) (store.Backend, error) {
	return &backend{
		serverAddress: u.Host,
		Probe:         probe.NewProbe(),
		updateHook:    fn,
	}, nil
}

// updateHookFn is a testing hook function
type updateHookFn func()

// Store offers store.StoreBackend interface through kubernetes custom resource definitions.
type backend struct {
	// mapping of CRD <> typeURLs.
	mapping *mapping

	// address of the MCP server.
	serverAddress string

	// The cancellation function that is used to cancel gRPC/MCP operations.
	cancel context.CancelFunc

	// The in-memory state, where resources are kept for out-of-band get and list calls.
	state *state

	// channel for publishing backend events. chLock is used to protect against ch lifecycle
	// events (e.g. creation of the channel).
	// It also protects against a race, where a channel could be created while there is incoming
	// data/changes.
	chLock sync.Mutex
	ch     chan store.BackendEvent

	// The update hook that was registered. This is for testing.
	updateHook updateHookFn

	*probe.Probe
}

var _ store.Backend = &backend{}
var _ probe.SupportsProbe = &backend{}
var _ client.Updater = &backend{}

// state is the in-memory cache.
type state struct {
	sync.Mutex

	// items stored by kind, then by key.
	items map[string]map[store.Key]*store.BackEndResource
}

// Init implements store.Backend.Init.
func (b *backend) Init(kinds []string) error {
	m, err := constructMapping(kinds, kube.Types)
	if err != nil {
		return err
	}
	b.mapping = m

	messageNames := b.mapping.messageNames()
	scope.Infof("Requesting following messages:")
	for i, name := range messageNames {
		scope.Infof("  [%d] %s", i, name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := grpc.DialContext(ctx, b.serverAddress, grpc.WithInsecure())
	if err != nil {
		cancel()
		scope.Errorf("Error connecting to server: %v\n", err)
		return err
	}
	cl := mcp.NewAggregatedMeshConfigServiceClient(conn)
	c := client.New(cl, messageNames, b, mixerNodeID, map[string]string{})
	configz.Register(c)

	b.state = &state{
		items: make(map[string]map[store.Key]*store.BackEndResource),
	}

	go c.Run(ctx)
	b.cancel = cancel

	return nil
}

// Stop implements store.backend.Stop.
func (b *backend) Stop() {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
}

// Watch creates a channel to receive the events.
func (b *backend) Watch() (<-chan store.BackendEvent, error) {
	b.chLock.Lock()
	defer b.chLock.Unlock()

	if b.ch != nil {
		return nil, fmt.Errorf("watch was already called")
	}

	b.ch = make(chan store.BackendEvent, eventChannelSize)

	return b.ch, nil
}

// Get returns a resource's spec to the key.
func (b *backend) Get(key store.Key) (*store.BackEndResource, error) {
	b.state.Lock()
	defer b.state.Unlock()

	perTypeState, found := b.state.items[key.Kind]
	if !found {
		return nil, store.ErrNotFound
	}

	item, found := perTypeState[key]
	if !found {
		return nil, store.ErrNotFound
	}

	return item, nil
}

// List returns the whole mapping from key to resource specs in the store.
func (b *backend) List() map[store.Key]*store.BackEndResource {
	b.state.Lock()
	defer b.state.Unlock()

	result := make(map[store.Key]*store.BackEndResource)
	for _, perTypeItems := range b.state.items {
		for k, v := range perTypeItems {
			result[k] = v
		}
	}

	return result
}

// Update implements client.Updater.Update.
func (b *backend) Update(change *client.Change) error {
	b.state.Lock()
	defer b.state.Unlock()
	defer b.callUpdateHook()

	newTypeStates := make(map[string]map[store.Key]*store.BackEndResource)
	typeURL := fmt.Sprintf("type.googleapis.com/%s", change.MessageName)

	scope.Debugf("Received update for: type:%s, count:%d", change.MessageName, len(change.Objects))

	for _, o := range change.Objects {
		var kind string
		var name string
		var contents proto.Message

		if scope.DebugEnabled() {
			scope.Debugf("Processing incoming resource: %q @%s [%s]", o.Metadata.Name, o.Version, o.MessageName)
		}

		// Demultiplex the resource, if it is a legacy type, and figure out its kind.
		if isLegacyTypeURL(typeURL) {
			// Extract the kind from payload.
			legacyResource := o.Resource.(*legacy.LegacyMixerResource)
			name = legacyResource.Name
			kind = legacyResource.Kind
			contents = legacyResource.Contents
		} else {
			// Otherwise, simply do a direct mapping from typeURL to kind
			name = o.Metadata.Name
			kind = b.mapping.kind(typeURL)
			contents = o.Resource
		}

		collection, found := newTypeStates[kind]
		if !found {
			collection = make(map[store.Key]*store.BackEndResource)
			newTypeStates[kind] = collection
		}

		// Map it to Mixer's store model, and put it in the new collection.

		key := toKey(kind, name)
		resource, err := toBackendResource(key, contents, o.Version)
		if err != nil {
			return err
		}
		collection[key] = resource
	}

	// Lock the channel state, as we will start publishing events soon.
	b.chLock.Lock()
	defer b.chLock.Unlock()

	// Now, diff against the in-memory state and generate store events.
	for kind, newTypeState := range newTypeStates {
		oldTypeState, found := b.state.items[kind]

		// Replace the old collection with the new one.
		// We can do this, because there is no error that can be raised from now on.
		b.state.items[kind] = newTypeState

		// If the downstream users haven't started listening yet, we don't need to
		// send any events.
		if b.ch == nil {
			continue
		}

		// Otherwise, start pumping events by diffing old and new states.
		if found {
			for k := range oldTypeState {
				if _, exists := newTypeState[k]; !exists {
					b.ch <- store.BackendEvent{Key: k, Type: store.Delete}
					continue
				}
			}
		}

		for k, v := range newTypeState {
			b.ch <- store.BackendEvent{Key: k, Type: store.Update, Value: v}
		}
	}

	return nil
}

func (b *backend) callUpdateHook() {
	if b.updateHook != nil {
		b.updateHook()
	}
}
