package server

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/traefik/traefik/v3/pkg/config/dynamic"
	"github.com/traefik/traefik/v3/pkg/provider/aggregator"
	"github.com/traefik/traefik/v3/pkg/safe"
	th "github.com/traefik/traefik/v3/pkg/testhelpers"
	"github.com/traefik/traefik/v3/pkg/tls"
)

type mockProvider struct {
	messages         []dynamic.Message
	wait             time.Duration
	first            chan struct{}
	throttleDuration time.Duration
}

func (p *mockProvider) Provide(configurationChan chan<- dynamic.Message, _ *safe.Pool) error {
	wait := p.wait
	if wait == 0 {
		wait = 20 * time.Millisecond
	}

	if len(p.messages) == 0 {
		return errors.New("no messages available")
	}

	configurationChan <- p.messages[0]

	if p.first != nil {
		<-p.first
	}

	for _, message := range p.messages[1:] {
		time.Sleep(wait)
		configurationChan <- message
	}

	return nil
}

// ThrottleDuration returns the throttle duration.
func (p *mockProvider) ThrottleDuration() time.Duration {
	return p.throttleDuration
}

func (p *mockProvider) Init() error {
	return nil
}

func TestNewConfigurationWatcher(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())
	t.Cleanup(routinesPool.Stop)

	pvd := &mockProvider{
		messages: []dynamic.Message{{
			ProviderName: "mock",
			Configuration: &dynamic.Configuration{
				HTTP: th.BuildConfiguration(
					th.WithRouters(
						th.WithRouter("test",
							th.WithEntryPoints("e"),
							th.WithServiceName("scv"))),
				),
			},
		}},
	}

	watcher := NewConfigurationWatcher(routinesPool, pvd, []string{}, "")

	run := make(chan struct{})

	watcher.AddListener(func(conf dynamic.Configuration) {
		expected := dynamic.Configuration{
			HTTP: th.BuildConfiguration(
				th.WithRouters(
					th.WithRouter("test@mock",
						th.WithEntryPoints("e"),
						th.WithServiceName("scv"),
						th.WithObservability())),
				th.WithMiddlewares(),
				th.WithLoadBalancerServices(),
			),
			TCP: &dynamic.TCPConfiguration{
				Routers:           map[string]*dynamic.TCPRouter{},
				Middlewares:       map[string]*dynamic.TCPMiddleware{},
				Services:          map[string]*dynamic.TCPService{},
				Models:            map[string]*dynamic.TCPModel{},
				ServersTransports: map[string]*dynamic.TCPServersTransport{},
			},
			TLS: &dynamic.TLSConfiguration{
				Options: map[string]tls.Options{
					"default": tls.DefaultTLSOptions,
				},
				Stores: map[string]tls.Store{},
			},
			UDP: &dynamic.UDPConfiguration{
				Routers:  map[string]*dynamic.UDPRouter{},
				Services: map[string]*dynamic.UDPService{},
			},
		}

		assert.Equal(t, expected, conf)
		close(run)
	})

	watcher.Start()
	<-run
}

func TestWaitForRequiredProvider(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	pvdAggregator := &mockProvider{
		wait: 5 * time.Millisecond,
	}

	config := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bar")),
		),
	}

	pvdAggregator.messages = append(pvdAggregator.messages, dynamic.Message{
		ProviderName:  "mock",
		Configuration: config,
	})

	pvdAggregator.messages = append(pvdAggregator.messages, dynamic.Message{
		ProviderName:  "required",
		Configuration: config,
	})

	pvdAggregator.messages = append(pvdAggregator.messages, dynamic.Message{
		ProviderName:  "mock2",
		Configuration: config,
	})

	watcher := NewConfigurationWatcher(routinesPool, pvdAggregator, []string{}, "required")

	publishedConfigCount := 0
	watcher.AddListener(func(_ dynamic.Configuration) {
		publishedConfigCount++
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// give some time so that the configuration can be processed
	time.Sleep(20 * time.Millisecond)

	// after 20 milliseconds we should have 2 configs published
	assert.Equal(t, 2, publishedConfigCount, "times configs were published")
}

func TestIgnoreTransientConfiguration(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	config := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bar")),
		),
	}

	expectedConfig := dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo@mock", th.WithEntryPoints("ep"), th.WithObservability())),
			th.WithLoadBalancerServices(th.WithService("bar@mock")),
			th.WithMiddlewares(),
		),
		TCP: &dynamic.TCPConfiguration{
			Routers:           map[string]*dynamic.TCPRouter{},
			Middlewares:       map[string]*dynamic.TCPMiddleware{},
			Services:          map[string]*dynamic.TCPService{},
			Models:            map[string]*dynamic.TCPModel{},
			ServersTransports: map[string]*dynamic.TCPServersTransport{},
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  map[string]*dynamic.UDPRouter{},
			Services: map[string]*dynamic.UDPService{},
		},
		TLS: &dynamic.TLSConfiguration{
			Options: map[string]tls.Options{
				"default": tls.DefaultTLSOptions,
			},
			Stores: map[string]tls.Store{},
		},
	}

	expectedConfig3 := dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo@mock", th.WithEntryPoints("ep"), th.WithObservability())),
			th.WithLoadBalancerServices(th.WithService("bar-config3@mock")),
			th.WithMiddlewares(),
		),
		TCP: &dynamic.TCPConfiguration{
			Routers:           map[string]*dynamic.TCPRouter{},
			Middlewares:       map[string]*dynamic.TCPMiddleware{},
			Services:          map[string]*dynamic.TCPService{},
			Models:            map[string]*dynamic.TCPModel{},
			ServersTransports: map[string]*dynamic.TCPServersTransport{},
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  map[string]*dynamic.UDPRouter{},
			Services: map[string]*dynamic.UDPService{},
		},
		TLS: &dynamic.TLSConfiguration{
			Options: map[string]tls.Options{
				"default": tls.DefaultTLSOptions,
			},
			Stores: map[string]tls.Store{},
		},
	}

	config2 := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("baz", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("toto")),
		),
	}

	config3 := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bar-config3")),
		),
	}
	watcher := NewConfigurationWatcher(routinesPool, &mockProvider{}, []string{}, "")

	// To be able to "block" the writes, we change the chan to remove buffering.
	watcher.allProvidersConfigs = make(chan dynamic.Message)

	publishedConfigCount := 0

	firstConfigHandled := make(chan struct{})
	blockConfConsumer := make(chan struct{})
	blockConfConsumerAssert := make(chan struct{})
	watcher.AddListener(func(config dynamic.Configuration) {
		publishedConfigCount++

		if publishedConfigCount > 2 {
			t.Fatal("More than 2 published configuration")
		}

		if publishedConfigCount == 1 {
			assert.Equal(t, expectedConfig, config)
			close(firstConfigHandled)

			<-blockConfConsumer
			time.Sleep(500 * time.Millisecond)
		}

		if publishedConfigCount == 2 {
			assert.Equal(t, expectedConfig3, config)
			close(blockConfConsumerAssert)
		}
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	watcher.allProvidersConfigs <- dynamic.Message{
		ProviderName:  "mock",
		Configuration: config,
	}

	<-firstConfigHandled

	watcher.allProvidersConfigs <- dynamic.Message{
		ProviderName:  "mock",
		Configuration: config2,
	}

	watcher.allProvidersConfigs <- dynamic.Message{
		ProviderName:  "mock",
		Configuration: config,
	}

	close(blockConfConsumer)

	watcher.allProvidersConfigs <- dynamic.Message{
		ProviderName:  "mock",
		Configuration: config3,
	}

	select {
	case <-blockConfConsumerAssert:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout")
	}
}

func TestListenProvidersThrottleProviderConfigReload(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	pvd := &mockProvider{
		wait:             10 * time.Millisecond,
		throttleDuration: 30 * time.Millisecond,
	}

	for i := range 5 {
		pvd.messages = append(pvd.messages, dynamic.Message{
			ProviderName: "mock",
			Configuration: &dynamic.Configuration{
				HTTP: th.BuildConfiguration(
					th.WithRouters(th.WithRouter("foo"+strconv.Itoa(i), th.WithEntryPoints("ep"))),
					th.WithLoadBalancerServices(th.WithService("bar")),
				),
			},
		})
	}

	providerAggregator := &aggregator.ProviderAggregator{}
	err := providerAggregator.AddProvider(pvd)
	assert.NoError(t, err)

	watcher := NewConfigurationWatcher(routinesPool, providerAggregator, []string{}, "")

	publishedConfigCount := 0
	watcher.AddListener(func(_ dynamic.Configuration) {
		publishedConfigCount++
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// Give some time so that the configuration can be processed.
	time.Sleep(100 * time.Millisecond)

	// To load 5 new configs it would require 150ms (5 configs * 30ms).
	// In 100ms, we should only have time to load 3 configs.
	assert.LessOrEqual(t, publishedConfigCount, 3, "config was applied too many times")
	assert.Positive(t, publishedConfigCount, "config was not applied at least once")
}

func TestListenProvidersSkipsEmptyConfigs(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	pvd := &mockProvider{
		messages: []dynamic.Message{{ProviderName: "mock"}},
	}

	watcher := NewConfigurationWatcher(routinesPool, pvd, []string{}, "")
	watcher.AddListener(func(_ dynamic.Configuration) {
		t.Error("An empty configuration was published but it should not")
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// give some time so that the configuration can be processed
	time.Sleep(100 * time.Millisecond)
}

func TestListenProvidersSkipsSameConfigurationForProvider(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	message := dynamic.Message{
		ProviderName: "mock",
		Configuration: &dynamic.Configuration{
			HTTP: th.BuildConfiguration(
				th.WithRouters(th.WithRouter("foo", th.WithEntryPoints("ep"))),
				th.WithLoadBalancerServices(th.WithService("bar")),
			),
		},
	}

	pvd := &mockProvider{
		messages: []dynamic.Message{message, message},
	}

	watcher := NewConfigurationWatcher(routinesPool, pvd, []string{}, "")

	var configurationReloads int
	watcher.AddListener(func(_ dynamic.Configuration) {
		configurationReloads++
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// give some time so that the configuration can be processed
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, configurationReloads, "Same configuration should not be published multiple times")
}

func TestListenProvidersDoesNotSkipFlappingConfiguration(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	configuration := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bar")),
		),
	}

	transientConfiguration := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("bad", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bad")),
		),
	}

	pvd := &mockProvider{
		wait:             5 * time.Millisecond, // The last message needs to be received before the second has been fully processed
		throttleDuration: 15 * time.Millisecond,
		messages: []dynamic.Message{
			{ProviderName: "mock", Configuration: configuration},
			{ProviderName: "mock", Configuration: transientConfiguration},
			{ProviderName: "mock", Configuration: configuration},
		},
	}

	watcher := NewConfigurationWatcher(routinesPool, pvd, []string{}, "")

	var lastConfig dynamic.Configuration
	watcher.AddListener(func(conf dynamic.Configuration) {
		lastConfig = conf
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// give some time so that the configuration can be processed
	time.Sleep(100 * time.Millisecond)

	expected := dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo@mock", th.WithEntryPoints("ep"), th.WithObservability())),
			th.WithLoadBalancerServices(th.WithService("bar@mock")),
			th.WithMiddlewares(),
		),
		TCP: &dynamic.TCPConfiguration{
			Routers:           map[string]*dynamic.TCPRouter{},
			Middlewares:       map[string]*dynamic.TCPMiddleware{},
			Services:          map[string]*dynamic.TCPService{},
			Models:            map[string]*dynamic.TCPModel{},
			ServersTransports: map[string]*dynamic.TCPServersTransport{},
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  map[string]*dynamic.UDPRouter{},
			Services: map[string]*dynamic.UDPService{},
		},
		TLS: &dynamic.TLSConfiguration{
			Options: map[string]tls.Options{
				"default": tls.DefaultTLSOptions,
			},
			Stores: map[string]tls.Store{},
		},
	}

	assert.Equal(t, expected, lastConfig)
}

func TestListenProvidersIgnoreSameConfig(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	configuration := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bar")),
		),
	}

	transientConfiguration := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("bad", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bad")),
		),
	}

	// The transient configuration is sent alternatively with the configuration we want to be applied.
	// It is intended to show that even if the configurations are different,
	// those transient configurations will be ignored if they are sent in a time frame
	// lower than the provider throttle duration.
	pvd := &mockProvider{
		wait:             1 * time.Microsecond, // Enqueue them fast
		throttleDuration: time.Millisecond,
		first:            make(chan struct{}),
		messages: []dynamic.Message{
			{ProviderName: "mock", Configuration: configuration},
			{ProviderName: "mock", Configuration: transientConfiguration},
			{ProviderName: "mock", Configuration: configuration},
			{ProviderName: "mock", Configuration: transientConfiguration},
			{ProviderName: "mock", Configuration: configuration},
		},
	}

	providerAggregator := &aggregator.ProviderAggregator{}
	err := providerAggregator.AddProvider(pvd)
	assert.NoError(t, err)

	watcher := NewConfigurationWatcher(routinesPool, providerAggregator, []string{}, "")

	var configurationReloads int
	var lastConfig dynamic.Configuration
	var once sync.Once
	watcher.AddListener(func(conf dynamic.Configuration) {
		configurationReloads++
		lastConfig = conf

		// Allows next configurations to be sent by the mock provider as soon as the first configuration message is applied.
		once.Do(func() {
			pvd.first <- struct{}{}
			// Wait for all configuration messages to pile in
			time.Sleep(5 * time.Millisecond)
		})
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// Wait long enough
	time.Sleep(50 * time.Millisecond)

	expected := dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo@mock", th.WithEntryPoints("ep"), th.WithObservability())),
			th.WithLoadBalancerServices(th.WithService("bar@mock")),
			th.WithMiddlewares(),
		),
		TCP: &dynamic.TCPConfiguration{
			Routers:           map[string]*dynamic.TCPRouter{},
			Middlewares:       map[string]*dynamic.TCPMiddleware{},
			Services:          map[string]*dynamic.TCPService{},
			Models:            map[string]*dynamic.TCPModel{},
			ServersTransports: map[string]*dynamic.TCPServersTransport{},
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  map[string]*dynamic.UDPRouter{},
			Services: map[string]*dynamic.UDPService{},
		},
		TLS: &dynamic.TLSConfiguration{
			Options: map[string]tls.Options{
				"default": tls.DefaultTLSOptions,
			},
			Stores: map[string]tls.Store{},
		},
	}

	assert.Equal(t, expected, lastConfig)

	assert.Equal(t, 1, configurationReloads)
}

func TestApplyConfigUnderStress(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	watcher := NewConfigurationWatcher(routinesPool, &mockProvider{}, []string{}, "")

	routinesPool.GoCtx(func(ctx context.Context) {
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case watcher.allProvidersConfigs <- dynamic.Message{ProviderName: "mock", Configuration: &dynamic.Configuration{
				HTTP: th.BuildConfiguration(
					th.WithRouters(th.WithRouter("foo"+strconv.Itoa(i), th.WithEntryPoints("ep"))),
					th.WithLoadBalancerServices(th.WithService("bar")),
				),
			}}:
			}
			i++
		}
	})

	var configurationReloads int
	watcher.AddListener(func(conf dynamic.Configuration) {
		configurationReloads++
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	time.Sleep(100 * time.Millisecond)

	// Ensure that at least two configurations have been applied
	// if we simulate being spammed configuration changes by the provider(s).
	// In theory, checking at least one would be sufficient,
	// but checking for two also ensures that we're looping properly,
	// and that the whole algo holds, etc.
	t.Log(configurationReloads)
	assert.GreaterOrEqual(t, configurationReloads, 2)
}

func TestListenProvidersIgnoreIntermediateConfigs(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	configuration := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bar")),
		),
	}

	transientConfiguration := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("bad", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bad")),
		),
	}

	transientConfiguration2 := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("bad2", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bad2")),
		),
	}

	finalConfiguration := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("final", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("final")),
		),
	}

	pvd := &mockProvider{
		wait:             10 * time.Microsecond, // Enqueue them fast
		throttleDuration: 10 * time.Millisecond,
		messages: []dynamic.Message{
			{ProviderName: "mock", Configuration: configuration},
			{ProviderName: "mock", Configuration: transientConfiguration},
			{ProviderName: "mock", Configuration: transientConfiguration2},
			{ProviderName: "mock", Configuration: finalConfiguration},
		},
	}

	providerAggregator := &aggregator.ProviderAggregator{}
	err := providerAggregator.AddProvider(pvd)
	assert.NoError(t, err)

	watcher := NewConfigurationWatcher(routinesPool, providerAggregator, []string{}, "")

	var configurationReloads int
	var lastConfig dynamic.Configuration
	watcher.AddListener(func(conf dynamic.Configuration) {
		configurationReloads++
		lastConfig = conf
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// Wait long enough
	time.Sleep(500 * time.Millisecond)

	expected := dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("final@mock", th.WithEntryPoints("ep"), th.WithObservability())),
			th.WithLoadBalancerServices(th.WithService("final@mock")),
			th.WithMiddlewares(),
		),
		TCP: &dynamic.TCPConfiguration{
			Routers:           map[string]*dynamic.TCPRouter{},
			Middlewares:       map[string]*dynamic.TCPMiddleware{},
			Services:          map[string]*dynamic.TCPService{},
			Models:            map[string]*dynamic.TCPModel{},
			ServersTransports: map[string]*dynamic.TCPServersTransport{},
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  map[string]*dynamic.UDPRouter{},
			Services: map[string]*dynamic.UDPService{},
		},
		TLS: &dynamic.TLSConfiguration{
			Options: map[string]tls.Options{
				"default": tls.DefaultTLSOptions,
			},
			Stores: map[string]tls.Store{},
		},
	}

	assert.Equal(t, expected, lastConfig)

	assert.Equal(t, 2, configurationReloads)
}

func TestListenProvidersPublishesConfigForEachProvider(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	configuration := &dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(th.WithRouter("foo", th.WithEntryPoints("ep"))),
			th.WithLoadBalancerServices(th.WithService("bar")),
		),
	}

	pvd := &mockProvider{
		messages: []dynamic.Message{
			{ProviderName: "mock", Configuration: configuration},
			{ProviderName: "mock2", Configuration: configuration},
		},
	}

	watcher := NewConfigurationWatcher(routinesPool, pvd, []string{}, "")

	var publishedProviderConfig dynamic.Configuration

	watcher.AddListener(func(conf dynamic.Configuration) {
		publishedProviderConfig = conf
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// give some time so that the configuration can be processed
	time.Sleep(100 * time.Millisecond)

	expected := dynamic.Configuration{
		HTTP: th.BuildConfiguration(
			th.WithRouters(
				th.WithRouter("foo@mock", th.WithEntryPoints("ep"), th.WithObservability()),
				th.WithRouter("foo@mock2", th.WithEntryPoints("ep"), th.WithObservability()),
			),
			th.WithLoadBalancerServices(
				th.WithService("bar@mock"),
				th.WithService("bar@mock2"),
			),
			th.WithMiddlewares(),
		),
		TCP: &dynamic.TCPConfiguration{
			Routers:           map[string]*dynamic.TCPRouter{},
			Middlewares:       map[string]*dynamic.TCPMiddleware{},
			Services:          map[string]*dynamic.TCPService{},
			Models:            map[string]*dynamic.TCPModel{},
			ServersTransports: map[string]*dynamic.TCPServersTransport{},
		},
		TLS: &dynamic.TLSConfiguration{
			Options: map[string]tls.Options{
				"default": tls.DefaultTLSOptions,
			},
			Stores: map[string]tls.Store{},
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  map[string]*dynamic.UDPRouter{},
			Services: map[string]*dynamic.UDPService{},
		},
	}

	assert.Equal(t, expected, publishedProviderConfig)
}

func TestPublishConfigUpdatedByProvider(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	pvdConfiguration := dynamic.Configuration{
		TCP: &dynamic.TCPConfiguration{
			Routers: map[string]*dynamic.TCPRouter{
				"foo": {},
			},
		},
	}

	pvd := &mockProvider{
		wait: 10 * time.Millisecond,
		messages: []dynamic.Message{
			{
				ProviderName:  "mock",
				Configuration: &pvdConfiguration,
			},
			{
				ProviderName:  "mock",
				Configuration: &pvdConfiguration,
			},
		},
	}

	watcher := NewConfigurationWatcher(routinesPool, pvd, []string{}, "")

	publishedConfigCount := 0
	watcher.AddListener(func(configuration dynamic.Configuration) {
		publishedConfigCount++

		// Update the provider configuration published in next dynamic Message which should trigger a new publishing.
		pvdConfiguration.TCP.Routers["bar"] = &dynamic.TCPRouter{}
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// give some time so that the configuration can be processed.
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, 2, publishedConfigCount)
}

func TestPublishConfigUpdatedByConfigWatcherListener(t *testing.T) {
	routinesPool := safe.NewPool(t.Context())

	pvd := &mockProvider{
		wait: 10 * time.Millisecond,
		messages: []dynamic.Message{
			{
				ProviderName: "mock",
				Configuration: &dynamic.Configuration{
					TCP: &dynamic.TCPConfiguration{
						Routers: map[string]*dynamic.TCPRouter{
							"foo": {},
						},
					},
				},
			},
			{
				ProviderName: "mock",
				Configuration: &dynamic.Configuration{
					TCP: &dynamic.TCPConfiguration{
						Routers: map[string]*dynamic.TCPRouter{
							"foo": {},
						},
					},
				},
			},
		},
	}

	watcher := NewConfigurationWatcher(routinesPool, pvd, []string{}, "")

	publishedConfigCount := 0
	watcher.AddListener(func(configuration dynamic.Configuration) {
		publishedConfigCount++

		// Modify the provided configuration.
		// This should not modify the configuration stored in the configuration watcher and therefore there will be no new publishing.
		configuration.TCP.Routers["foo@mock"].Rule = "bar"
	})

	watcher.Start()

	t.Cleanup(watcher.Stop)
	t.Cleanup(routinesPool.Stop)

	// give some time so that the configuration can be processed.
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, 1, publishedConfigCount)
}
