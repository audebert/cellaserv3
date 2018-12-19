package cellaserv

import (
	"context"
	"encoding/json"
	"fmt"

	cellaserv "bitbucket.org/evolutek/cellaserv2-protobuf"
	"bitbucket.org/evolutek/cellaserv3/broker"
	"bitbucket.org/evolutek/cellaserv3/broker/cellaserv/api"
	"bitbucket.org/evolutek/cellaserv3/client"
	"bitbucket.org/evolutek/cellaserv3/common"

	logging "github.com/op/go-logging"
)

// Options for the cellaserv service
type Options struct {
	BrokerAddr string
}

// Cellaserv service
type Cellaserv struct {
	options *Options
	broker  *broker.Broker
	logger  *logging.Logger

	registeredCh chan struct{}
}

// whoami sends back the client info of the sender
func (cs *Cellaserv) whoami(req *cellaserv.Request) (interface{}, error) {
	client, err := cs.broker.GetRequestSender(req)
	if err != nil {
		return nil, err
	}
	return client.JSONStruct(), nil
}

// nameClient attaches a name to the client that sent the request.
func (cs *Cellaserv) nameClient(req *cellaserv.Request) (interface{}, error) {
	var data api.NameClientRequest
	err := json.Unmarshal(req.Data, &data)
	if err != nil {
		cs.logger.Warningf("[Cellaserv] Could not unmarshal request data: %s, %s", req.Data, err)
		return nil, err
	}

	// Ask the broker to do the rename
	cs.broker.RenameClientFromRequest(req, data.Name)

	return nil, nil
}

// listClients replies with the list of currently connected clients
func (cs *Cellaserv) listClients(*cellaserv.Request) (interface{}, error) {
	return cs.broker.GetClientsJSON(), nil
}

// listServices retuns the list of services in the broker
func (cs *Cellaserv) listServices(*cellaserv.Request) (interface{}, error) {
	return cs.broker.GetServicesJSON(), nil
}

// listEvents replies with the list of subscribers
func (cs *Cellaserv) listEvents(*cellaserv.Request) (interface{}, error) {
	return cs.broker.GetEventsJSON(), nil
}

// shutdown quits the broker
func (cs *Cellaserv) shutdown(*cellaserv.Request) (interface{}, error) {
	cs.logger.Info("[Cellaserv] Shutting down.")
	close(cs.broker.Quit())
	return nil, nil
}

// handleSpy registers the connection as a `py of a service
func (cs *Cellaserv) handleSpy(req *cellaserv.Request) (interface{}, error) {
	var data api.SpyRequest
	err := json.Unmarshal(req.Data, &data)
	if err != nil {
		cs.logger.Warningf("[Cellaserv] Could not spy: %s", err)
		return nil, err
	}

	srvc, err := cs.broker.GetService(data.ServiceName, data.ServiceIdentification)
	if err != nil {
		return nil, err
	}

	client, ok := cs.broker.GetClient(data.ClientId)
	if !ok {
		cs.logger.Warningf("[Cellaserv] Could not spy, no such service: %s %s", data.ServiceName,
			data.ServiceIdentification)
		return nil, fmt.Errorf("No such service: %s[%s]", data.ServiceName, data.ServiceIdentification)
	}
	cs.broker.SpyService(client, srvc)

	return nil, nil
}

// version return the version of cellaserv
func version(req *cellaserv.Request) (interface{}, error) {
	return common.Version, nil
}

func (cs *Cellaserv) getLogs(req *cellaserv.Request) (interface{}, error) {
	var data api.GetLogsRequest
	err := json.Unmarshal(req.Data, &data)
	if err != nil {
		cs.logger.Warningf("[Cellaserv] Invalid get_logs() request: %s", err)
		return nil, err
	}

	logs, err := cs.broker.GetLogsByPattern(data.Pattern)
	if err != nil {
		cs.logger.Warningf("[Cellaserv] Could not get logs: %s", err)
		return nil, err
	}

	return logs, nil
}

func (cs *Cellaserv) Run(ctx context.Context) error {
	// Wait for broker to be ready
	select {
	case <-cs.broker.Started():
		break
	case <-ctx.Done():
		return nil
	}

	// Create the cellaserv service
	c := client.NewClient(client.ClientOpts{CellaservAddr: cs.options.BrokerAddr})
	service := c.NewService("cellaserv", "")
	service.HandleRequestFunc("whoami", cs.whoami)
	service.HandleRequestFunc("name_client", cs.nameClient)
	service.HandleRequestFunc("list_clients", cs.listClients)
	service.HandleRequestFunc("list_services", cs.listServices)
	service.HandleRequestFunc("list_events", cs.listEvents)
	service.HandleRequestFunc("get_logs", cs.getLogs)
	service.HandleRequestFunc("shutdown", cs.shutdown)
	service.HandleRequestFunc("version", version)

	// Run the service
	c.RegisterService(service)
	close(cs.registeredCh)

	select {
	case <-c.Quit():
		return nil
	case <-ctx.Done():
		return nil
	}
}

func New(options *Options, broker *broker.Broker, logger *logging.Logger) *Cellaserv {
	return &Cellaserv{
		options:      options,
		broker:       broker,
		logger:       logger,
		registeredCh: make(chan struct{}),
	}
}