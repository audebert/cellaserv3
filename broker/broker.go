package broker

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	cellaserv "bitbucket.org/evolutek/cellaserv2-protobuf"
	"github.com/evolutek/cellaserv3/common"
	"github.com/golang/protobuf/proto"
	logging "gopkg.in/op/go-logging.v1"
)

type Options struct {
	ListenAddress string
}

type Broker struct {
	logger *logging.Logger

	Options *Options

	// Socket where all incoming connections go
	mainListener net.Listener

	// List of all currently handled connections
	connList *list.List

	// Map a connection to a name, filled with cellaserv.descrbie-conn
	connNameMap map[net.Conn]string

	// Map a connection to the service it spies
	connSpies map[net.Conn][]*service

	// Map of currently connected services by name, then identification
	Services map[string]map[string]*service

	// Map of all services associated with a connection
	servicesConn map[net.Conn][]*service

	// Map of requests ids with associated timeout timer
	reqIds             map[uint64]*requestTracking
	subscriberMap      map[string][]net.Conn
	subscriberMatchMap map[string][]net.Conn
}

// Manage incoming connexions
func (b *Broker) handle(conn net.Conn) {
	b.logger.Info("[Broker] Connection opened: %s", b.connDescribe(conn))

	connJSON := connToJSON(conn)
	b.cellaservPublish(logNewConnection, connJSON)

	// Append to list of handled connections
	connListElt := b.connList.PushBack(conn)

	// Handle all messages received on this connection
	for {
		closed, msgBytes, msg, err := common.RecvMessage(conn)
		if err != nil {
			b.logger.Error("[Message] Receive: %s", err)
		}
		if closed {
			b.logger.Info("[Broker] Connection closed: %s", b.connDescribe(conn))
			break
		}
		err = b.handleMessage(conn, msgBytes, msg)
		if err != nil {
			b.logger.Error("[Message] Handle: %s", err)
		}
	}

	// Remove from list of handled connection
	b.connList.Remove(connListElt)

	// Clean connection name, if not given this is a noop
	delete(b.connNameMap, conn)

	// Remove services registered by this connection
	// TODO: notify goroutines waiting for acks for this service
	for _, s := range b.servicesConn[conn] {
		b.logger.Info("[Services] Remove %s", s)
		pubJSON, _ := json.Marshal(s.JSONStruct())
		b.cellaservPublish(logLostService, pubJSON)
		delete(b.Services[s.Name], s.Identification)

		// Close connections that spied this service
		for _, c := range s.Spies {
			b.logger.Debug("[Service] Close spy conn: %s", b.connDescribe(c))
			if err := c.Close(); err != nil {
				b.logger.Error("Could not close connection:", err)
			}
		}
	}
	delete(b.servicesConn, conn)

	// Remove subscribes from this connection
	removeConnFromMap := func(subMap map[string][]net.Conn) {
		for key, subs := range subMap {
			for i, subConn := range subs {
				if conn == subConn {
					// Remove from list of subscribers
					subs[i] = subs[len(subs)-1]
					subMap[key] = subs[:len(subs)-1]

					pubJSON, _ := json.Marshal(
						logSubscriberJSON{key, b.connDescribe(conn)})
					b.cellaservPublish(logLostSubscriber, pubJSON)

					if len(subMap[key]) == 0 {
						delete(subMap, key)
						break
					}
				}
			}
		}
	}
	removeConnFromMap(b.subscriberMap)
	removeConnFromMap(b.subscriberMatchMap)

	// Remove conn from the services it spied
	for _, srvc := range b.connSpies[conn] {
		for i, connItem := range srvc.Spies {
			if connItem == conn {
				// Remove from slice
				srvc.Spies[i] = srvc.Spies[len(srvc.Spies)-1]
				srvc.Spies = srvc.Spies[:len(srvc.Spies)-1]
				break
			}
		}
	}
	delete(b.connSpies, conn)

	b.cellaservPublish(logCloseConnection, connJSON)
}

func (b *Broker) logUnmarshalError(msg []byte) {
	dbg := ""
	for _, b := range msg {
		dbg = dbg + fmt.Sprintf("0x%02X ", b)
	}
	b.logger.Error("[Broker] Bad message (%d bytes): %s", len(msg), dbg)
}

func (b *Broker) handleMessage(conn net.Conn, msgBytes []byte, msg *cellaserv.Message) error {
	var err error

	// Parse and process message payload
	msgContent := msg.GetContent()

	switch msg.GetType() {
	case cellaserv.Message_Register:
		register := &cellaserv.Register{}
		err = proto.Unmarshal(msgContent, register)
		if err != nil {
			b.logUnmarshalError(msgContent)
			return fmt.Errorf("Could not unmarshal register: %s", err)
		}
		b.handleRegister(conn, register)
		return nil
	case cellaserv.Message_Request:
		request := &cellaserv.Request{}
		err = proto.Unmarshal(msgContent, request)
		if err != nil {
			b.logUnmarshalError(msgContent)
			return fmt.Errorf("Could not unmarshal request: %s", err)
		}
		b.handleRequest(conn, msgBytes, request)
		return nil
	case cellaserv.Message_Reply:
		reply := &cellaserv.Reply{}
		err = proto.Unmarshal(msgContent, reply)
		if err != nil {
			b.logUnmarshalError(msgContent)
			return fmt.Errorf("Could not unmarshal reply: %s", err)
		}
		b.handleReply(conn, msgBytes, reply)
		return nil
	case cellaserv.Message_Subscribe:
		sub := &cellaserv.Subscribe{}
		err = proto.Unmarshal(msgContent, sub)
		if err != nil {
			b.logUnmarshalError(msgContent)
			return fmt.Errorf("Could not unmarshal subscribe: %s", err)
		}
		b.handleSubscribe(conn, sub)
		return nil
	case cellaserv.Message_Publish:
		pub := &cellaserv.Publish{}
		err = proto.Unmarshal(msgContent, pub)
		if err != nil {
			b.logUnmarshalError(msgContent)
			return fmt.Errorf("Could not unmarshal publish: %s", err)
		}
		b.handlePublish(conn, msgBytes, pub)
		return nil
	default:
		return fmt.Errorf("Unknown message type: %d", msg.Type)
	}
}

// listenAndServe starts the cellaserv broker
func (b *Broker) listenAndServe(sockAddrListen string) error {
	// Create TCP listenener for incoming connections
	var err error
	b.mainListener, err = net.Listen("tcp", sockAddrListen)
	if err != nil {
		b.logger.Error("[Broker] Could not listen: %s", err)
		return err
	}

	b.logger.Info("[Broker] Listening on %s", sockAddrListen)

	// Handle new connections
	for {
		conn, err := b.mainListener.Accept()
		nerr, ok := err.(net.Error)
		if ok {
			if nerr.Temporary() {
				b.logger.Warning("[Broker] Could not accept: %s", err)
				time.Sleep(10 * time.Millisecond)
				continue
			} else {
				b.logger.Error("[Broker] Connection unavailable: %s", err)
				break
			}
		}

		go b.handle(conn)
	}

	return nil
}

func (b *Broker) Run(ctx context.Context) error {
	// Configure CPU profiling, stopped when cellaserv receive the kill request
	b.setupProfiling()

	return b.listenAndServe(b.Options.ListenAddress)
}

func New(logger *logging.Logger, options *Options) *Broker {
	return &Broker{
		logger:             logger,
		Options:            options,
		connNameMap:        make(map[net.Conn]string),
		connSpies:          make(map[net.Conn][]*service),
		Services:           make(map[string]map[string]*service),
		servicesConn:       make(map[net.Conn][]*service),
		reqIds:             make(map[uint64]*requestTracking),
		subscriberMap:      make(map[string][]net.Conn),
		subscriberMatchMap: make(map[string][]net.Conn),
		connList:           list.New(),
	}
}
