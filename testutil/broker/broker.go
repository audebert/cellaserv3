package broker

import (
	"context"
	"testing"
	"time"

	"bitbucket.org/evolutek/cellaserv3/broker"
	"bitbucket.org/evolutek/cellaserv3/client"
	logging "github.com/op/go-logging"
)

func WithTestBroker(t *testing.T, listenAddress string, testFn func(client.ClientOpts)) {
	ctxBroker, cancelBroker := context.WithCancel(context.Background())
	brokerOptions := &broker.Options{ListenAddress: listenAddress}
	broker := broker.New(brokerOptions, logging.MustGetLogger("broker"))

	go func() {
		err := broker.Run(ctxBroker)
		if err != nil {
			t.Fatalf("Could not start broker: %s", err)
		}
	}()

	// Give time to the broker to start
	time.Sleep(50 * time.Millisecond)

	// Run the test
	testFn(client.ClientOpts{CellaservAddr: listenAddress})
	time.Sleep(50 * time.Millisecond)

	// Teardown broker
	cancelBroker()
	time.Sleep(50 * time.Millisecond)
}